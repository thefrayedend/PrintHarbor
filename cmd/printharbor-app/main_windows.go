//go:build windows

// printharbor-app is the desktop app window: a native window with the
// PrintHarbor interface (and the model sites) inside it, using the Edge
// WebView2 engine that ships with Windows. It keeps its own isolated browser
// profile, so it never touches your normal browsing, and files model downloads
// into your library by itself — no browser extension.
//
// It is a thin shell over the PrintHarbor server: if the server isn't already
// running, it starts it, waits for the port, then opens the window.
//
// Files: main_windows.go (this window), boot.js (the script injected into every
// page), boot.go and appcfg.go (helpers that also build and test off Windows).
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	webview "github.com/jchv/go-webview2"
)

// Where the PrintHarbor server is. The port comes from config.json (see
// loadAppConfig), so changing "listen" there no longer breaks the window.
var (
	cfg    appConfig
	addr   string // 127.0.0.1:<port>, to see whether the server is up
	origin string // http://localhost:<port>
	appURL string // origin + "/?app=1": marks the UI as the app window
)

func portUp() bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// startServer launches the PrintHarbor server binary sitting next to us (or
// one folder up, where the launcher keeps the exe in dist\).
func startServer() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)
	for _, cand := range []string{
		filepath.Join(dir, "printharbor-windows-amd64.exe"),
		filepath.Join(dir, "printharbor.exe"),
		filepath.Join(filepath.Dir(dir), "dist", "printharbor-windows-amd64.exe"),
		filepath.Join(filepath.Dir(dir), "printharbor-windows-amd64.exe"),
	} {
		if _, err := os.Stat(cand); err == nil {
			c := exec.Command(cand)
			c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
			_ = c.Start()
			return
		}
	}
}

// openInBrowser is the fallback when WebView2 can't start (runtime missing).
func openInBrowser() {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", origin).Start()
}

func main() {
	self, _ := os.Executable()
	exeDir := ""
	if self != "" {
		exeDir = filepath.Dir(self)
	}
	cfg = loadAppConfig(exeDir, os.Args[1:])
	addr = "127.0.0.1:" + cfg.Port
	origin = "http://localhost:" + cfg.Port
	appURL = origin + "/?app=1"
	selfStamp, haveStamp := stampOf(self)
	go removeOldSelf(self)

	if !portUp() {
		startServer()
		for i := 0; i < 180 && !portUp(); i++ {
			time.Sleep(time.Second)
		}
	}

	// Keep a profile of our own so the app's browsing is separate from the
	// user's normal browser (and their site logins here persist).
	dataPath := ""
	if base, err := os.UserConfigDir(); err == nil {
		dataPath = filepath.Join(base, "PrintHarbor", "AppView")
		_ = os.MkdirAll(dataPath, 0o755)
	}

	w := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		DataPath:  dataPath,
		WindowOptions: webview.WindowOptions{
			Title:  "PrintHarbor",
			Width:  1280,
			Height: 840,
			Center: true,
		},
	})
	if w == nil {
		// WebView2 runtime isn't installed: fall back to the browser so the
		// user still gets PrintHarbor.
		openInBrowser()
		return
	}
	defer w.Destroy()
	// The injected capture script (boot.js) calls window.phImport with the
	// download it caught; this runs it in the app process and posts it to the
	// local server, which bypasses the browser's cross-origin / private-network
	// rules that block a model-site page from reaching localhost directly.
	client := &http.Client{Timeout: 30 * time.Second} // the server answers at once; the download runs there
	_ = w.Bind("phImport", func(payload string) (string, error) {
		resp, err := client.Post(origin+"/api/sites/capture-url", "application/json", strings.NewReader(payload))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode >= 400 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(b, &e)
			msg := strings.TrimSpace(e.Error)
			if msg == "" {
				msg = resp.Status
			}
			return "", errors.New(msg)
		}
		return "Downloading into your PrintHarbor library…", nil
	})
	// phLog lets the injected script write to data\app-log.txt so we can see what
	// happens inside the window (it has no console we can reach otherwise). The
	// file is kept to about half a megabyte.
	_ = w.Bind("phLog", func(msg string) string {
		if cfg.DataDir != "" {
			if len(msg) > 2000 {
				msg = msg[:2000]
			}
			appendCapped(filepath.Join(cfg.DataDir, "app-log.txt"), time.Now().Format("15:04:05")+" "+msg, 512<<10, 128<<10)
		}
		return "ok"
	})
	// phOwnPage: boot.js calls it each time PrintHarbor's own page loads in this
	// window (see reopenWhenUpdated).
	_ = w.Bind("phOwnPage", func() string {
		ownPageAt.Store(time.Now().UnixNano())
		return "ok"
	})
	w.Init(bootScript(origin))
	w.Navigate(appURL)
	maximize(w) // open filling the screen
	if haveStamp {
		go reopenWhenUpdated(w, self, selfStamp)
	}
	w.Run()
}

// ownPageAt is when PrintHarbor's own page last loaded in this window
// (Unix nanoseconds; 0 = not yet).
var ownPageAt atomic.Int64

// reopenWhenUpdated makes "Restart PrintHarbor" the only step of an update.
// A waiting update for this window (printharbor-app.new.exe) is swapped in by
// the server when it restarts: it renames this running program out of the way
// and puts the new one in its place. The window you are looking at would keep
// running the old program until you closed it. So when it sees that its file
// was replaced, it waits for a safe moment (PrintHarbor's own page has just
// loaded here, which it does by itself after a restart, so nothing unsaved is
// open), then opens the new window and closes itself.
func reopenWhenUpdated(w webview.WebView, self string, was fileStamp) {
	for !replaced(self, was) {
		time.Sleep(2 * time.Second)
	}
	noticed := time.Now()
	for {
		at := ownPageAt.Load()
		if at != 0 && safeToReopen(noticed, time.Unix(0, at)) && portUp() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	c := exec.Command(self, os.Args[1:]...)
	c.Dir = filepath.Dir(self)
	if err := c.Start(); err != nil {
		return // couldn't start the new one: keep this window (the page says what to do)
	}
	w.Dispatch(func() { w.Terminate() })
}

// removeOldSelf deletes the copy of this program a previous update left behind
// (printharbor-app.exe.old). The window that was running it may still be
// closing, so it tries for a little while.
func removeOldSelf(self string) {
	if self == "" {
		return
	}
	old := self + ".old"
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(old); err != nil {
			return
		}
		if os.Remove(old) == nil {
			return
		}
		time.Sleep(time.Second)
	}
}

// maximize opens the window maximized (fills the screen, keeps the title bar so
// it's still easy to move or close).
func maximize(w webview.WebView) {
	if hwnd := w.Window(); hwnd != nil {
		const swMaximize = 3
		procShowWindow.Call(uintptr(hwnd), swMaximize)
	}
}

var procShowWindow = syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")
