package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// PrintHarbor has no login: anyone who can reach it on your network can use
// it, which is the point (phone, tablet, another PC). What must NOT be able to
// use it is some web page you happen to have open in a browser. Without this
// guard any site could quietly POST to http://localhost:7621 from your browser
// and send G-code to a printer, change the Orca path, or install a plugin.
//
// Two checks:
//
//  1. Changes (POST/PUT/DELETE/PATCH) must come from PrintHarbor's own pages.
//     A browser always says which site a cross-site request comes from (the
//     Origin header), so one that names another site is refused. Programs that
//     aren't browsers (the app window's own import, slicing workers, curl)
//     send no Origin and are unaffected. PrintHarbor's browser extension is
//     allowed on its one endpoint.
//
//  2. Requests that arrive from this computer itself must be addressed to a
//     local name (localhost, an IP address, harbor.local, the PC's name). This
//     stops "DNS rebinding", where a web page points its own domain name at
//     127.0.0.1 so the browser treats PrintHarbor as part of that site.
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fromLoopback(r) && !a.localHostName(r.Host) {
			http.Error(w, "PrintHarbor: this address isn't one of PrintHarbor's names. Open http://localhost"+portSuffix(r.Host)+" instead (or add the name to \"allowed_hosts\" in config.json).", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !a.sameSite(r) {
				fail(w, http.StatusForbidden, fmt.Errorf("this request came from another website, so PrintHarbor refused it"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func portSuffix(host string) string {
	if _, p, err := net.SplitHostPort(host); err == nil && p != "" {
		return ":" + p
	}
	return ""
}

func fromLoopback(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		h = r.RemoteAddr
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func hostOnly(hostport string) string {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
}

// localHostName reports whether a Host header is a name PrintHarbor answers to
// on this computer.
func (a *App) localHostName(hostport string) bool {
	h := hostOnly(hostport)
	if h == "" || h == "localhost" || net.ParseIP(h) != nil {
		return true // HTTP/1.0 without Host, localhost, or a plain IP address
	}
	if !strings.Contains(h, ".") {
		return true // the PC's own name ("desktop-abc")
	}
	for _, suf := range []string{".local", ".localhost", ".lan", ".home", ".home.arpa", ".internal", ".localdomain"} {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	if u, err := url.Parse(a.cfg().PublicURL); err == nil && u.Hostname() != "" && strings.EqualFold(u.Hostname(), h) {
		return true
	}
	for _, ok := range a.cfg().AllowedHosts {
		if strings.EqualFold(strings.TrimSpace(ok), h) {
			return true
		}
	}
	return false
}

// sameSite reports whether a state-changing request comes from PrintHarbor's
// own pages (or from something that isn't a web page at all).
func (a *App) sameSite(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		// No Origin: not a cross-site browser request, unless the browser says so.
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	if strings.HasPrefix(o, "chrome-extension://") || strings.HasPrefix(o, "moz-extension://") {
		return r.URL.Path == "/api/capture" // the extension's one endpoint (it checks the rest itself)
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false // "null" (sandboxed page, file://) or junk
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// Behind a reverse proxy the Host can differ from what the browser sees.
	if pu, err := url.Parse(a.cfg().PublicURL); err == nil && pu.Host != "" && strings.EqualFold(pu.Host, u.Host) {
		return true
	}
	return false
}

// publicIP reports whether an address is on the public internet (not this
// computer, not the home network, not a link-local or reserved range).
func publicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 (carrier NAT / Tailscale), 0.0.0.0/8, 192.0.0.0/24, 198.18.0.0/15, 240.0.0.0/4
		switch {
		case v4[0] == 100 && v4[1]&0xc0 == 64, v4[0] == 0, v4[0] == 192 && v4[1] == 0 && v4[2] == 0,
			v4[0] == 198 && (v4[1] == 18 || v4[1] == 19), v4[0] >= 240:
			return false
		}
	}
	return true
}

// publicOnlyDial is the dial check for fetching things a web page (or a model
// site's data) told us to fetch: it refuses, after the name has been looked
// up, to connect to this computer or the home network. Checking the name alone
// isn't enough, because any name can be made to point at 127.0.0.1.
var publicOnlyDial = func(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if !publicIP(net.ParseIP(host)) {
		return fmt.Errorf("refusing to fetch from a private address (%s)", host)
	}
	return nil
}

// publicTransport picks the checked connection, unless the computer is set up
// to reach the internet through a proxy (then the proxy does the looking up,
// and it is the only thing we can connect to).
type publicTransport struct{ checked, proxied *http.Transport }

func (t publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if u, err := http.ProxyFromEnvironment(r); err == nil && u != nil {
		return t.proxied.RoundTrip(r)
	}
	return t.checked.RoundTrip(r)
}

var publicRT = func() publicTransport {
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error { return publicOnlyDial(network, address, c) }}
	mk := func() *http.Transport {
		return &http.Transport{TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConns: 8, IdleConnTimeout: 60 * time.Second}
	}
	checked, proxied := mk(), mk()
	checked.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
	proxied.Proxy = http.ProxyFromEnvironment
	return publicTransport{checked, proxied}
}()

// publicClient fetches only from the public internet over https, including
// across redirects.
func publicClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: publicRT, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("the download redirected to a link that isn't https")
		}
		return nil
	}}
}
