package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/mdns"
)

// Can a phone on the same network reach PrintHarbor? The usual thing in the
// way is Windows Firewall: it blocks incoming connections to a program unless
// a rule allows that program on the kind of network the PC is on (Private or
// Public). PrintHarbor can't see a blocked connection, so it reads the
// firewall's own answer (netsh) and says what it found, in Settings and in the
// log. Reading only; the one change it can make is the "Allow PrintHarbor"
// button, which asks Windows for permission (the UAC prompt) first.

// Reach is what /api/info reports about being reachable from other devices.
type Reach struct {
	// State: "allowed", "blocked", "off" (the firewall is off for this
	// network), or "unknown" (not Windows, or the firewall could not be read).
	State   string `json:"state"`
	Profile string `json:"profile,omitempty"` // Private | Public | Domain
	Why     string `json:"why,omitempty"`     // one plain sentence
	CanFix  bool   `json:"can_fix,omitempty"`
}

type fwRule struct {
	Name     string
	Enabled  bool
	Allow    bool
	Profiles []string // lower case: private, public, domain; empty = any
}

var reFwField = regexp.MustCompile(`^([A-Za-z ]+?):\s+(.*)$`)

// parseFwRules reads "netsh advfirewall firewall show rule name=all dir=in
// verbose" and returns the rules that apply to the program exe.
func parseFwRules(out, exe string) []fwRule {
	var rules []fwRule
	var cur map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		if strings.EqualFold(strings.TrimSpace(cur["program"]), exe) {
			r := fwRule{Name: cur["rule name"], Enabled: strings.EqualFold(cur["enabled"], "yes"), Allow: strings.EqualFold(cur["action"], "allow")}
			for _, p := range strings.Split(cur["profiles"], ",") {
				if p = strings.ToLower(strings.TrimSpace(p)); p != "" && p != "any" {
					r.Profiles = append(r.Profiles, p)
				}
			}
			rules = append(rules, r)
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		m := reFwField.FindStringSubmatch(strings.TrimRight(line, " "))
		if m == nil {
			continue
		}
		k, v := strings.ToLower(strings.TrimSpace(m[1])), strings.TrimSpace(m[2])
		if k == "rule name" {
			flush()
			cur = map[string]string{}
		}
		if cur != nil {
			cur[k] = v
		}
	}
	flush()
	return rules
}

var reFwProfile = regexp.MustCompile(`(?m)^(Domain|Private|Public) Profile Settings:`)
var reFwState = regexp.MustCompile(`(?m)^State\s+(ON|OFF)\b`)
var reFwPolicy = regexp.MustCompile(`(?m)^Firewall Policy\s+(\S+)`)

// parseFwProfile reads "netsh advfirewall show currentprofile": which kind of
// network the PC is on, whether the firewall is on for it, and whether it lets
// incoming connections in when no rule says otherwise.
func parseFwProfile(out string) (profile string, on, allowInbound, ok bool) {
	out = strings.ReplaceAll(out, "\r", "")
	p := reFwProfile.FindStringSubmatch(out)
	s := reFwState.FindStringSubmatch(out)
	if p == nil || s == nil {
		return "", false, false, false
	}
	pol := reFwPolicy.FindStringSubmatch(out)
	return p[1], s[1] == "ON", pol != nil && strings.HasPrefix(strings.ToLower(pol[1]), "allowinbound"), true
}

// fwVerdict decides what the firewall does with incoming connections to the
// program on the current kind of network. A block rule wins over an allow
// rule, as it does in Windows.
func fwVerdict(rules []fwRule, profile string, on, allowInbound bool) Reach {
	if !on {
		return Reach{State: "off", Profile: profile, Why: "Windows Firewall is off for this network, so it is not in the way."}
	}
	applies := func(r fwRule) bool {
		if !r.Enabled {
			return false
		}
		if len(r.Profiles) == 0 {
			return true
		}
		for _, p := range r.Profiles {
			if strings.EqualFold(p, profile) {
				return true
			}
		}
		return false
	}
	var allow, block, elsewhere bool
	for _, r := range rules {
		switch {
		case applies(r) && r.Allow:
			allow = true
		case applies(r):
			block = true
		case r.Enabled && r.Allow:
			elsewhere = true
		}
	}
	switch {
	case block:
		return Reach{State: "blocked", Profile: profile, CanFix: true, Why: "Windows Firewall has a rule that blocks PrintHarbor on " + profile + " networks, and this PC's network is set to " + profile + ". Phones and other devices can't connect."}
	case allow || allowInbound:
		return Reach{State: "allowed", Profile: profile, Why: "Windows Firewall allows PrintHarbor on this network (" + profile + ")."}
	case elsewhere:
		return Reach{State: "blocked", Profile: profile, CanFix: true, Why: "Windows Firewall allows PrintHarbor only on another kind of network, and this PC's network is set to " + profile + ". Phones and other devices can't connect."}
	}
	return Reach{State: "blocked", Profile: profile, CanFix: true, Why: "Windows Firewall has no rule allowing PrintHarbor on " + profile + " networks, so it blocks phones and other devices."}
}

var reach struct {
	mu   sync.Mutex
	at   time.Time
	last Reach
}

// reachNow returns the firewall's answer, read at most every 20 seconds (netsh
// takes a second or two).
func reachNow(force bool) Reach {
	reach.mu.Lock()
	defer reach.mu.Unlock()
	if !force && !reach.at.IsZero() && time.Since(reach.at) < 20*time.Second {
		return reach.last
	}
	exe, _ := os.Executable()
	reach.last, reach.at = readFirewall(exe), time.Now()
	return reach.last
}

// lanAddresses is this computer's addresses on the network, for "type this
// into your phone".
func lanAddresses() []string {
	out := []string{}
	for _, ip := range mdns.LocalIPv4() {
		out = append(out, ip.String())
	}
	return out
}

func (a *App) reachRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/reach", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"firewall": reachNow(r.URL.Query().Get("fresh") == "1"), "addresses": lanAddresses(), "port": listenPort(a.cfg().Listen), "name": a.cfg().MDNSName})
	})
	// Adds the firewall rule. Only from the PC itself: it makes Windows show
	// its permission prompt there, and someone has to be at the PC to answer.
	mux.HandleFunc("POST /api/reach/allow", func(w http.ResponseWriter, r *http.Request) {
		if !fromLoopback(r) {
			fail(w, 403, errString("Do this on the PC that runs PrintHarbor: Windows asks for permission there."))
			return
		}
		exe, _ := os.Executable()
		if err := allowInFirewall(exe, reachNow(false).Profile); err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"firewall": reachNow(true)})
	})
}

type errString string

func (e errString) Error() string { return string(e) }

// ReachSummary is one line for the log: the addresses to type on a phone and
// what the firewall does.
func ReachSummary(port string) string {
	line := "Addresses for other devices:"
	for _, ip := range lanAddresses() {
		line += " http://" + ip + ":" + port
	}
	if r := reachNow(false); r.State != "unknown" {
		line += ". " + r.Why
	}
	return line
}
