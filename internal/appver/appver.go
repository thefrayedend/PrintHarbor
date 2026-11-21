// Package appver holds the build number of the desktop app window
// (cmd/printharbor-app), shared by the window and the server.
//
// The window tells every page its build (window.__phAppBuild, see boot.js) and
// the server reports the build it was made alongside (/api/info "app_build").
// When a window that is still open is older than that, the page asks you to
// close and reopen it. Normally you never see that: after an update the window
// reopens itself.
//
// Bump Build whenever anything under cmd/printharbor-app changes (use the
// number of the versions\ folder the new printharbor-app.exe goes into).
// Leave it alone when only the server changes.
package appver

const Build = 65
