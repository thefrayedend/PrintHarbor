package api

import "testing"

const fwRulesSample = "\r\nRule Name:                            Core Networking - DNS\r\n----------------------------------------------------------------------\r\nEnabled:                              Yes\r\nDirection:                            In\r\nProfiles:                             Domain,Private,Public\r\nProgram:                              C:\\Windows\\system32\\svchost.exe\r\nAction:                               Allow\r\n\r\nRule Name:                            printharbor-windows-amd64.exe\r\n----------------------------------------------------------------------\r\nDescription:                          printharbor-windows-amd64.exe\r\nEnabled:                              Yes\r\nDirection:                            In\r\nProfiles:                             Public\r\nGrouping:                             \r\nLocalIP:                              Any\r\nRemoteIP:                             Any\r\nProtocol:                             TCP\r\nLocalPort:                            Any\r\nRemotePort:                           Any\r\nEdge traversal:                       No\r\nProgram:                              C:\\PrintHarbor\\printharbor-windows-amd64.exe\r\nInterfaceTypes:                       Any\r\nSecurity:                             NotRequired\r\nRule source:                          Local Setting\r\nAction:                               Block\r\n\r\nRule Name:                            PrintHarbor old\r\n----------------------------------------------------------------------\r\nEnabled:                              Yes\r\nDirection:                            In\r\nProfiles:                             Private\r\nProgram:                              c:\\printharbor\\printharbor-windows-amd64.exe\r\nAction:                               Allow\r\nOk.\r\n"

const fwProfileSample = "\r\nPublic Profile Settings: \r\n----------------------------------------------------------------------\r\nState                                 ON\r\nFirewall Policy                       BlockInbound,AllowOutbound\r\nLocalFirewallRules                    N/A (GPO-store only)\r\nOk.\r\n"

func TestFirewallReading(t *testing.T) {
	exe := `C:\PrintHarbor\printharbor-windows-amd64.exe`
	rules := parseFwRules(fwRulesSample, exe)
	if len(rules) != 2 || rules[0].Allow || !rules[0].Enabled || len(rules[0].Profiles) != 1 || rules[0].Profiles[0] != "public" || !rules[1].Allow {
		t.Fatalf("rules read wrong: %+v", rules)
	}
	profile, on, allowIn, ok := parseFwProfile(fwProfileSample)
	if !ok || profile != "Public" || !on || allowIn {
		t.Fatalf("profile read wrong: %q on=%v allowIn=%v ok=%v", profile, on, allowIn, ok)
	}
	// Public network: the block rule applies, the allow rule is for Private only.
	if v := fwVerdict(rules, "Public", true, false); v.State != "blocked" || !v.CanFix {
		t.Fatalf("public: %+v", v)
	}
	// Private network: only the allow rule applies.
	if v := fwVerdict(rules, "Private", true, false); v.State != "allowed" {
		t.Fatalf("private: %+v", v)
	}
	// Allowed elsewhere only, no rule here: blocked.
	if v := fwVerdict(rules[1:], "Public", true, false); v.State != "blocked" {
		t.Fatalf("allowed on private only, pc on public: %+v", v)
	}
	// No rule at all: blocked by default; allowed when the policy lets things in; off is off.
	if v := fwVerdict(nil, "Private", true, false); v.State != "blocked" {
		t.Fatalf("no rule: %+v", v)
	}
	if v := fwVerdict(nil, "Private", true, true); v.State != "allowed" {
		t.Fatalf("allow-inbound policy: %+v", v)
	}
	if v := fwVerdict(rules, "Public", false, false); v.State != "off" {
		t.Fatalf("firewall off: %+v", v)
	}
	if _, _, _, ok := parseFwProfile("garbage"); ok {
		t.Fatal("garbage was understood")
	}
}
