package main

import (
	_ "embed"
	"strconv"
	"strings"

	"github.com/thefrayedend/printharbor/internal/appver"
)

// boot.js is the script the app window runs at the top of every page (see the
// comment at the top of that file). It lives in its own file so it can be read,
// edited and tested as JavaScript instead of as one long Go string.
//
//go:embed boot.js
var bootJS string

// bootScript is boot.js with PrintHarbor's own address (origin is like
// "http://localhost:7621") and this window's build number filled in.
func bootScript(origin string) string {
	return strings.NewReplacer("__PH_ORIGIN__", origin, "__PH_APP_BUILD__", strconv.Itoa(appver.Build)).Replace(bootJS)
}
