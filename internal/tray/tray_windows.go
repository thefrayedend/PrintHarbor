//go:build windows

package tray

import (
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	pAppendMenuW              = user32.NewProc("AppendMenuW")
	pSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pDestroyMenu              = user32.NewProc("DestroyMenu")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	pCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	pRegOpenKeyExW            = advapi32.NewProc("RegOpenKeyExW")
	pRegSetValueExW           = advapi32.NewProc("RegSetValueExW")
	pRegQueryValueExW         = advapi32.NewProc("RegQueryValueExW")
	pRegDeleteValueW          = advapi32.NewProc("RegDeleteValueW")
	pRegCloseKey              = advapi32.NewProc("RegCloseKey")
)

const (
	wmDestroy      = 0x0002
	wmClose        = 0x0010
	wmNull         = 0x0000
	wmCommand      = 0x0111
	wmLButtonUp    = 0x0202
	wmLButtonDbl   = 0x0203
	wmRButtonUp    = 0x0205
	wmApp          = 0x8000
	wmTray         = wmApp + 1
	nimAdd         = 0
	nimModify      = 1
	nimDelete      = 2
	nifMessage     = 0x1
	nifIcon        = 0x2
	nifTip         = 0x4
	nifInfo        = 0x10
	mfString       = 0x0
	mfChecked      = 0x8
	mfSeparator    = 0x800
	tpmReturnCmd   = 0x100
	tpmRightButton = 0x2
	tpmBottomAlign = 0x20
)

const (
	idOpen = 1 + iota
	idRestart
	idAutoStart
	idLog
	idQuit
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func copyU16(dst []uint16, s string) {
	w, _ := syscall.UTF16FromString(s)
	if len(w) > len(dst) {
		w = w[:len(dst)]
		w[len(w)-1] = 0
	}
	copy(dst, w)
}

type tray struct {
	m              Menu
	hwnd           uintptr
	icon           uintptr
	taskbarCreated uintptr
	lastOpen       time.Time
}

// Run shows the tray icon. The icon's window lives on its own locked OS
// thread with a message loop. stop removes the icon.
func Run(m Menu) (stop func()) {
	t := &tray{m: m}
	ready := make(chan struct{})
	go t.loop(ready)
	<-ready
	return func() {
		if t.hwnd != 0 {
			pPostMessageW.Call(t.hwnd, wmClose, 0, 0)
		}
	}
}

func (t *tray) loop(ready chan struct{}) {
	runtime.LockOSThread()
	hInst, _, _ := pGetModuleHandleW.Call(0)
	png := iconPNG(32)
	t.icon, _, _ = pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)), 1, 0x00030000, 32, 32, 0)
	t.taskbarCreated, _, _ = pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(u16("TaskbarCreated"))))

	cls := u16("PrintHarborTray")
	wc := wndClassEx{LpfnWndProc: syscall.NewCallback(t.wndProc), HInstance: hInst, LpszClassName: cls, HIcon: t.icon, HIconSm: t.icon}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	t.hwnd, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("PrintHarbor"))), 0, 0, 0, 0, 0, 0, 0, hInst, 0)
	t.add(t.m.Balloon)
	close(ready)

	var ms msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&ms)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&ms)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&ms)))
	}
}

func (t *tray) nid() notifyIconData {
	n := notifyIconData{HWnd: t.hwnd, UID: 1}
	n.CbSize = uint32(unsafe.Sizeof(n))
	return n
}

func (t *tray) add(balloon string) {
	n := t.nid()
	n.UFlags = nifMessage | nifIcon | nifTip
	n.UCallbackMessage = wmTray
	n.HIcon = t.icon
	copyU16(n.SzTip[:], t.m.Tooltip)
	if balloon != "" {
		n.UFlags |= nifInfo
		copyU16(n.SzInfo[:], balloon)
		copyU16(n.SzInfoTitle[:], "PrintHarbor")
		n.DwInfoFlags = 1 // NIIF_INFO
	}
	pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&n)))
}

func (t *tray) remove() {
	n := t.nid()
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
}

func (t *tray) wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmTray:
		switch lParam & 0xffff {
		case wmLButtonUp, wmLButtonDbl:
			// a double-click also sends two button-ups: open the page once
			if time.Since(t.lastOpen) > 2*time.Second {
				t.lastOpen = time.Now()
				go call(t.m.OnOpen)
			}
		case wmRButtonUp:
			t.menu()
		}
		return 0
	case wmClose:
		t.remove()
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	if t.taskbarCreated != 0 && message == t.taskbarCreated {
		t.add("") // Explorer restarted: put the icon back
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func call(f func()) {
	if f != nil {
		f()
	}
}

func (t *tray) menu() {
	h, _, _ := pCreatePopupMenu.Call()
	add := func(id int, flags uintptr, text string) {
		pAppendMenuW.Call(h, flags, uintptr(id), uintptr(unsafe.Pointer(u16(text))))
	}
	add(idOpen, mfString, "Open PrintHarbor")
	add(idRestart, mfString, "Restart PrintHarbor")
	add(0, mfSeparator, "")
	if t.m.AutoStart != nil {
		var f uintptr = mfString
		if t.m.AutoStart() {
			f |= mfChecked
		}
		add(idAutoStart, f, "Start with Windows")
	}
	if t.m.OnLog != nil {
		add(idLog, mfString, "Open log file")
	}
	add(0, mfSeparator, "")
	add(idQuit, mfString, "Quit PrintHarbor")
	pSetMenuDefaultItem.Call(h, idOpen, 0)

	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmReturnCmd|tpmRightButton|tpmBottomAlign, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	pPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	pDestroyMenu.Call(h)

	switch int(cmd) {
	case idOpen:
		go call(t.m.OnOpen)
	case idRestart:
		go call(t.m.OnRestart)
	case idLog:
		go call(t.m.OnLog)
	case idQuit:
		go call(t.m.OnQuit)
	case idAutoStart:
		if t.m.SetAutoStart != nil {
			on := !t.m.AutoStart()
			go func() {
				if err := t.m.SetAutoStart(on); err != nil {
					Alert("PrintHarbor", "Couldn't change \"Start with Windows\": "+err.Error())
				}
			}()
		}
	}
}

// Alert shows a message box. With no console window, this is how startup
// errors reach the user.
func Alert(title, text string) {
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), 0x30) // MB_ICONWARNING
}

// ---- Start with Windows (HKCU\...\Run) ----

const (
	hkcu     = 0x80000001
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	keyRead  = 0x20019
	keyWrite = 0x20006
)

func AutoStartSupported() bool { return true }

func openRun(access uintptr) (uintptr, error) {
	var h uintptr
	r, _, _ := pRegOpenKeyExW.Call(hkcu, uintptr(unsafe.Pointer(u16(runKey))), 0, access, uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return h, nil
}

// AutoStartEnabled reports whether the Run entry exists.
func AutoStartEnabled(name string) bool {
	h, err := openRun(keyRead)
	if err != nil {
		return false
	}
	defer pRegCloseKey.Call(h)
	r, _, _ := pRegQueryValueExW.Call(h, uintptr(unsafe.Pointer(u16(name))), 0, 0, 0, 0)
	return r == 0
}

// SetAutoStart adds or removes the Run entry that starts cmd at sign-in.
func SetAutoStart(name, cmd string, on bool) error {
	h, err := openRun(keyRead | keyWrite)
	if err != nil {
		return err
	}
	defer pRegCloseKey.Call(h)
	if !on {
		r, _, _ := pRegDeleteValueW.Call(h, uintptr(unsafe.Pointer(u16(name))))
		if r != 0 && r != 2 { // 2 = not found
			return syscall.Errno(r)
		}
		return nil
	}
	data, _ := syscall.UTF16FromString(cmd)
	r, _, _ := pRegSetValueExW.Call(h, uintptr(unsafe.Pointer(u16(name))), 0, 1, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}
