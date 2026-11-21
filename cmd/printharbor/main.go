// PrintHarbor: one project, every printer in your workshop.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	printharbor "github.com/thefrayedend/printharbor"
	"github.com/thefrayedend/printharbor/internal/api"
	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/mdns"
	"github.com/thefrayedend/printharbor/internal/tray"
)

const autoStartName = "PrintHarbor"

func main() {
	cfgPath := flag.String("config", "", "path to config.json (created if missing; default: next to the program)")
	version := flag.Bool("version", false, "print version and exit")
	open := flag.Bool("open", false, "open PrintHarbor in the browser once it's ready")
	restarted := flag.Bool("restarted", false, "internal: started by Restart, wait for the old copy to let go of the port")
	noTray := flag.Bool("no-tray", false, "don't show the Windows tray icon")
	flag.Parse()
	if *version {
		fmt.Println("PrintHarbor", api.VersionLabel())
		return
	}

	// Work from the folder that holds config.json, so relative paths in it
	// (data, library) mean the same thing however PrintHarbor was started:
	// the launcher, a double-click on the exe, or "Start with Windows".
	cfgFile := findConfig(*cfgPath)
	if err := os.Chdir(filepath.Dir(cfgFile)); err != nil {
		fatal("can't use the folder %s: %v", filepath.Dir(cfgFile), err)
	}
	cleanupOldExe()

	cfg, err := config.Load(cfgFile)
	if err != nil {
		fatal("config: %v", err)
	}
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	logPath, _ := filepath.Abs(filepath.Join(cfg.DataDir, "printharbor.log"))
	_, port, _ := net.SplitHostPort(cfg.Listen)
	if port == "" {
		port = "7621"
	}
	localURL := "http://localhost:" + port

	// Claim the port first, so a second copy notices right away instead of
	// loading everything and then failing. After a restart the old copy may
	// need a moment to let go of it.
	ln, err := net.Listen("tcp", cfg.Listen)
	for i := 0; err != nil && *restarted && i < 40; i++ {
		time.Sleep(250 * time.Millisecond)
		ln, err = net.Listen("tcp", cfg.Listen)
	}
	if err != nil {
		if alreadyRunning(localURL) {
			log.Printf("PrintHarbor is already running at %s", localURL)
			openBrowser(localURL) // double-clicking the exe again just opens the page
			return
		}
		fatal("can't use port %s (%v). Another program is using it: change \"listen\" in %s", port, err, cfgFile)
	}

	// Keep a log file next to the data so problems can be looked at later.
	// Without a console (the Windows tray build) the file is the only log.
	// Only now that the port is ours: a second copy (double-clicking the exe
	// while PrintHarbor is running) must not wipe the running copy's log.
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if !*restarted {
		// Keep the last run's log: after a crash or a start that went wrong,
		// that is the one you need, and starting again used to wipe it.
		if fi, err := os.Stat(logPath); err == nil && fi.Size() > 0 {
			prev := strings.TrimSuffix(logPath, ".log") + ".prev.log"
			_ = os.Remove(prev)
			_ = os.Rename(logPath, prev)
		}
	}
	if *restarted {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND // keep the lines from before the restart
	}
	if f, err := os.OpenFile(logPath, flags, 0o644); err == nil {
		log.SetOutput(teeWriter{f, os.Stderr})
		defer f.Close()
	}
	// A downloaded update waits next to the program as <name>.new.exe. Apply it
	// on ANY start — not only the in-app "Restart" — so quitting and reopening
	// the app, or launching it from a shortcut or "Start with Windows", installs
	// it too. We are the primary instance here (we hold the port). An update for
	// the app window alone is installed here as well and needs no hand-off.
	//
	// An update that can't be installed (a virus scanner holding the file, a
	// read-only folder) must never stop PrintHarbor from starting: it is logged
	// and the version already here runs. The update stays waiting for next time.
	if !*restarted {
		swapped, undo, err := installUpdates()
		switch {
		case err != nil:
			log.Printf("Couldn't install the waiting update (%v). Starting the version already here; the update stays waiting.", err)
		case swapped:
			// Start the new copy first (it waits for the port), then let go of it.
			if err := spawn(*open); err != nil {
				undo()
				log.Printf("Couldn't start the updated program (%v). Put the old one back and starting it.", err)
			} else {
				_ = ln.Close()
				return
			}
		}
	}
	log.Printf("PrintHarbor %s starting…", api.VersionLabel())
	app, err := api.New(cfg)
	if err != nil {
		fatal("startup: %v", err)
	}
	app.CfgPath = cfgFile
	app.StartedAt = time.Now()
	go func() {
		r := app.Library.Rescan()
		log.Printf("Library: scanned %d folder(s): %d new, %d updated, %d removed", r.Folders, r.Added, r.Updated, r.Removed)
	}()
	lib := app.Lib()
	log.Printf("Orca: %s", cfg.OrcaPath)
	log.Printf("Profiles: %d printers, %d processes, %d filaments from %s",
		len(lib.List("machine")), len(lib.List("process")), len(lib.List("filament")), cfg.OrcaProfilesDir)
	for _, e := range lib.Errors {
		log.Printf("profile warning: %s", e)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	restart := make(chan struct{}, 1)
	app.Restart = func() {
		select {
		case restart <- struct{}{}:
		default:
		}
	}
	go app.Run(ctx)
	if name := cfg.MDNSName; name != "" && name != "-" {
		go func() {
			if err := mdns.Run(ctx, name); err != nil {
				log.Printf("harbor.local name not available (%v); use the IP address instead", err)
			}
		}()
		log.Printf("On other devices: http://%s.local:%s", name, port)
	}

	srv := &http.Server{Handler: app.Handler(printharbor.Web()), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Printf("Ready: %s", localURL)

	// Also answer on the ordinary web port, so http://harbor.local works
	// without ":7621" on the end. Best effort: another web server on this
	// computer may already have port 80, and then only the full address works.
	if port != "80" && !cfg.NoPort80 {
		go func() {
			host, _, _ := net.SplitHostPort(cfg.Listen)
			var ln80 net.Listener
			var err error
			for i := 0; i < 40; i++ { // after a restart the old copy needs a moment to let go
				if ln80, err = net.Listen("tcp", net.JoinHostPort(host, "80")); err == nil || !*restarted {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if err != nil {
				log.Printf("Port 80 isn't free (%v), so the short address without :%s won't work; the full address does.", err, port)
				return
			}
			log.Printf("Also on port 80: the address works without :%s", port)
			_ = srv.Serve(ln80)
		}()
	}

	removeTray := func() {}
	if runtime.GOOS == "windows" && !*noTray {
		exe, _ := os.Executable()
		balloon := ""
		if !*restarted {
			balloon = "PrintHarbor is running. Click this icon to open it; right-click for Restart and Quit."
		}
		removeTray = tray.Run(tray.Menu{
			Tooltip:   "PrintHarbor " + api.VersionLabel() + " — " + localURL,
			Balloon:   balloon,
			OnOpen:    func() { openBrowser(localURL) },
			OnRestart: app.Restart,
			OnLog:     func() { openFile(logPath) },
			OnQuit:    stop,
			AutoStart: func() bool { return tray.AutoStartEnabled(autoStartName) },
			SetAutoStart: func(on bool) error {
				return tray.SetAutoStart(autoStartName, fmt.Sprintf(`"%s" -config "%s"`, exe, cfgFile), on)
			},
		})
	}
	if *open {
		openBrowser(localURL)
	}

	restarting := false
	select {
	case <-ctx.Done():
		log.Printf("Shutting down…")
	case <-restart:
		restarting = true
		log.Printf("Restarting…")
	case err := <-served:
		removeTray()
		fatal("%v", err)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(sctx) // also closes the listener, freeing the port
	cancel()
	stop() // stops polling, mDNS and the slicer worker
	removeTray()
	if restarting && os.Getenv("INVOCATION_ID") != "" {
		// Under systemd: exit and let the service manager start us again
		// (needs Restart=on-failure or always in the unit).
		os.Exit(75)
	}
	if restarting {
		if err := relaunch(); err != nil {
			log.Printf("ERROR: restart failed: %v", err)
			tray.Alert("PrintHarbor", "PrintHarbor couldn't restart: "+err.Error()+"\n\nStart it again from the Start menu or the launcher.")
			os.Exit(1)
		}
	}
	time.Sleep(200 * time.Millisecond) // let the tray icon disappear
}

// findConfig picks config.json: the -config flag, else one in the current
// folder, else one next to the program or the folder above it (the launcher
// keeps the exe in dist\), else a new one next to the program.
func findConfig(flagValue string) string {
	abs := func(p string) string {
		a, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		return a
	}
	if flagValue != "" {
		return abs(flagValue)
	}
	if _, err := os.Stat("config.json"); err == nil {
		return abs("config.json")
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, d := range []string{dir, filepath.Dir(dir)} {
			if _, err := os.Stat(filepath.Join(d, "config.json")); err == nil {
				return filepath.Join(d, "config.json")
			}
		}
		if filepath.Base(dir) == "dist" {
			return filepath.Join(filepath.Dir(dir), "config.json")
		}
		return filepath.Join(dir, "config.json")
	}
	return abs("config.json")
}

// selfPath is this program's own file.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe2, err := filepath.EvalSymlinks(exe); err == nil {
		exe = exe2
	}
	return exe, nil
}

// swapIn puts a waiting <name>.new.exe in the place of <name>.exe: the current
// file is renamed to <name>.exe.old (Windows lets a running program be renamed,
// just not overwritten) and the new one takes its name. It reports whether
// there was an update, and returns a function that puts things back.
func swapIn(exe string) (swapped bool, undo func(), err error) {
	ext := filepath.Ext(exe)
	next := strings.TrimSuffix(exe, ext) + ".new" + ext
	fi, err := os.Stat(next)
	if err != nil {
		return false, func() {}, nil
	}
	// Never swap in something that can't be a program (an empty or half-made
	// file, a folder): that would leave nothing to start.
	if !fi.Mode().IsRegular() || fi.Size() < 1024 {
		return false, func() {}, fmt.Errorf("%s isn't a complete program file", filepath.Base(next))
	}
	old := exe + ".old"
	_ = os.Remove(old)
	hadOld := false
	if _, err := os.Stat(exe); err == nil {
		if err := os.Rename(exe, old); err != nil {
			return false, func() {}, err
		}
		hadOld = true
	}
	if err := os.Rename(next, exe); err != nil {
		if hadOld {
			_ = os.Rename(old, exe)
		}
		return false, func() {}, err
	}
	return true, func() {
		_ = os.Rename(exe, next)
		if hadOld {
			_ = os.Rename(old, exe)
		}
	}, nil
}

// installUpdates swaps in whatever updates are waiting next to the program:
// the server itself, and the desktop app window (printharbor-app), each on its
// own, so an update for only one of them is installed too. The app window is a
// separate process; its new copy is picked up when the window next opens (the
// window reopens itself, see cmd/printharbor-app). It reports whether the
// SERVER was replaced (then a fresh copy must be started) and how to undo that.
// err is about the server only; an app-window problem is logged, not returned.
func installUpdates() (serverSwapped bool, undo func(), err error) {
	undo = func() {}
	exe, err := selfPath()
	if err != nil {
		return false, undo, err
	}
	appExe := filepath.Join(filepath.Dir(exe), "printharbor-app"+filepath.Ext(exe))
	if ok, _, aerr := swapIn(appExe); aerr != nil {
		log.Printf("couldn't install the app window update: %v", aerr)
	} else if ok {
		log.Printf("Installed the app window update (%s)", filepath.Base(appExe))
	}
	serverSwapped, undo, err = swapIn(exe)
	if err != nil {
		return false, func() {}, fmt.Errorf("can't replace %s: %v", filepath.Base(exe), err)
	}
	if serverSwapped {
		log.Printf("Installed the update (%s)", filepath.Base(exe))
	}
	return serverSwapped, undo, nil
}

// spawn starts a fresh copy of this program with the same arguments. The copy
// is told it follows a running one (-restarted), so it waits for the port.
// keepOpen keeps a -open the launcher asked for (on a normal start the page
// should still open after an update; after tray → Restart it should not).
func spawn(keepOpen bool) error {
	exe, err := selfPath()
	if err != nil {
		return err
	}
	var args []string
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-restarted") || strings.HasPrefix(a, "--restarted") {
			continue
		}
		if (a == "-open" || a == "--open") && !keepOpen {
			continue
		}
		args = append(args, a)
	}
	args = append(args, "-restarted")
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Start()
}

// relaunch is tray → Restart: install waiting updates, then start a fresh copy.
// A server update that can't be installed is logged and the current version is
// started again: a restart must always bring PrintHarbor back.
func relaunch() error {
	_, undo, err := installUpdates()
	if err != nil {
		log.Printf("Couldn't install the waiting update (%v). Restarting the version already here.", err)
	}
	if err := spawn(false); err != nil {
		undo()
		return err
	}
	return nil
}

// cleanupOldExe deletes the program file a previous update left behind.
// The old copy may still be exiting, so it tries for a little while.
func cleanupOldExe() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	old := exe + ".old"
	if _, err := os.Stat(old); err != nil {
		return
	}
	go func() {
		for i := 0; i < 30; i++ {
			if err := os.Remove(old); err == nil || errors.Is(err, os.ErrNotExist) {
				return
			}
			time.Sleep(time.Second)
		}
	}()
}

// teeWriter writes to every writer, ignoring ones that fail (a GUI program
// on Windows has no usable stderr, which must not stop the log file).
type teeWriter []io.Writer

func (t teeWriter) Write(p []byte) (int, error) {
	for _, w := range t {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

// alreadyRunning checks whether the program on the port is PrintHarbor.
func alreadyRunning(url string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url + "/api/info")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var info struct {
		Version string `json:"version"`
	}
	return json.NewDecoder(resp.Body).Decode(&info) == nil && info.Version != ""
}

func openBrowser(url string) { openFile(url) }

// openFile opens a web address or file with the system's default program.
func openFile(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Open %s yourself", target)
	}
}

func fatal(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	log.Printf("ERROR: %s", msg)
	if runtime.GOOS == "windows" {
		tray.Alert("PrintHarbor can't start", msg)
	}
	os.Exit(1)
}
