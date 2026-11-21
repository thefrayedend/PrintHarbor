//go:build windows

package api

// "Install as desktop app": write a .ico and create Desktop + Start Menu
// shortcuts that open PrintHarbor. Edge/Chrome open it as a chromeless app
// window (its own taskbar icon, looks like a native app); Firefox opens it in
// a window using the normal profile/session; "default" makes a plain browser
// shortcut. Windows blocks programs from pinning to the taskbar themselves, so
// the user right-clicks the shortcut and chooses "Pin to taskbar".

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/thefrayedend/printharbor/internal/tray"
)

// installRoot is where PrintHarbor lives (next to config.json), a good home
// for the icon file.
func (a *App) installRoot() string {
	if a.CfgPath != "" {
		return filepath.Dir(a.CfgPath)
	}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		if filepath.Base(d) == "dist" {
			return filepath.Dir(d)
		}
		return d
	}
	return a.Store.Dir()
}

const shortcutPS = `param([string]$Engine,[string]$Url,[string]$Icon,[string]$Name,[string]$Target)
$ErrorActionPreference='Stop'
function Find-Browser($exe){
  foreach($root in 'HKLM:','HKCU:'){
    try{ $p=(Get-ItemProperty "$root\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\$exe" -ErrorAction SilentlyContinue).'(default)'; if($p -and (Test-Path $p)){ return $p } }catch{}
  }
  foreach($c in @(
    "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
    "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
    "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
    "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe",
    "$env:ProgramFiles\Mozilla Firefox\firefox.exe",
    "${env:ProgramFiles(x86)}\Mozilla Firefox\firefox.exe")){
    if(($c -like "*$exe") -and (Test-Path $c)){ return $c }
  }
  return $null
}
$exe=$null; $arguments=$null
switch($Engine){
  'edge'    { $exe=Find-Browser 'msedge.exe'; if(-not $exe){ $exe=Find-Browser 'chrome.exe' }; $arguments="--app=$Url" }
  'chrome'  { $exe=Find-Browser 'chrome.exe'; if(-not $exe){ $exe=Find-Browser 'msedge.exe' }; $arguments="--app=$Url" }
  'firefox' { $exe=Find-Browser 'firefox.exe'; $arguments="-new-window $Url" }
  'app'     { if($Target -and (Test-Path $Target)){ $exe=$Target; $arguments='' } }
  default   { $exe=$null }
}
$sh=New-Object -ComObject WScript.Shell
$targets=@([Environment]::GetFolderPath('Desktop'), (Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs'))
$made=@()
foreach($dir in $targets){
  if(-not (Test-Path $dir)){ continue }
  if($exe){
    $lnk=Join-Path $dir "$Name.lnk"
    $s=$sh.CreateShortcut($lnk)
    $s.TargetPath=$exe; $s.Arguments=$arguments
    $s.IconLocation="$Icon,0"; $s.WorkingDirectory=(Split-Path $exe); $s.Description='PrintHarbor'
    $s.Save(); $made+=$lnk
  } else {
    $u=Join-Path $dir "$Name.url"
    @('[InternetShortcut]',"URL=$Url","IconFile=$Icon",'IconIndex=0') | Set-Content -LiteralPath $u -Encoding ASCII
    $made+=$u
  }
}
if($made.Count -eq 0){ throw 'could not write the shortcut' }
if(-not $exe){ Write-Output "URL $($made -join '|')" } else { Write-Output "APP $($made -join '|')" }
`

// makeShortcut writes the icon and runs the PowerShell that creates the
// shortcuts. It returns a short message for the user.
func (a *App) makeShortcut(engine string) (string, error) {
	switch engine {
	case "edge", "chrome", "firefox", "default", "app":
	default:
		engine = "app"
	}
	root := a.installRoot()
	_ = os.MkdirAll(root, 0o755)
	icoPath := filepath.Join(root, "printharbor.ico")
	if err := os.WriteFile(icoPath, tray.IcoBytes(), 0o644); err != nil {
		return "", fmt.Errorf("couldn't write the icon: %w", err)
	}
	psPath := filepath.Join(a.Store.Dir(), "make-shortcut.ps1")
	if err := os.WriteFile(psPath, []byte(shortcutPS), 0o644); err != nil {
		return "", err
	}
	url := fmt.Sprintf("http://localhost:%s", listenPort(a.cfg().Listen))
	target := ""
	if engine == "app" {
		for _, cand := range []string{
			filepath.Join(root, "dist", "printharbor-app.exe"),
			filepath.Join(root, "printharbor-app.exe"),
		} {
			if _, err := os.Stat(cand); err == nil {
				target = cand
				break
			}
		}
		if target == "" {
			return "", fmt.Errorf("the app window program (printharbor-app.exe) isn't installed yet")
		}
	}
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", psPath, "-Engine", engine, "-Url", url, "-Icon", icoPath, "-Name", "PrintHarbor", "-Target", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.CombinedOutput()
	res := strings.TrimSpace(string(out))
	if err != nil {
		if res == "" {
			res = err.Error()
		}
		return "", fmt.Errorf("couldn't create the shortcut: %s", res)
	}
	msg := "Added PrintHarbor to your Desktop and Start menu. Right-click it and choose \"Pin to taskbar\"."
	switch {
	case engine == "app":
		msg += " It opens the PrintHarbor app window."
	case strings.HasPrefix(res, "APP"):
		msg += " It opens in its own app window."
	default:
		msg += " It opens in your default browser."
	}
	return msg, nil
}
