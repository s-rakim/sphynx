package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

// DefaultPolicy mirrors the policy a new Tailscale tailnet starts with, plus
// the rules Taildrive needs.
const DefaultPolicy = `// Sphynx access policy.
// It uses Tailscale's policy syntax: https://tailscale.com/docs/reference/syntax/policy-file
{
	// Groups let you write rules for several people at once.
	// "groups": {"group:family": ["alice@", "bob@"]},

	// Tags name devices that provide a service, e.g. "tag:server".
	// "tagOwners": {"tag:server": ["autogroup:member"]},

	"grants": [
		// Every device can reach every other device (Tailscale's default).
		{"src": ["*"], "dst": ["*"], "ip": ["*"]},

		// Taildrive: people can open folders shared from their own devices.
		{
			"src": ["autogroup:member"],
			"dst": ["autogroup:self"],
			"app": {"tailscale.com/cap/drive": [{"shares": ["*"], "access": "rw"}]},
		},
	],

	// Tailscale SSH: log in to your own devices without SSH keys.
	"ssh": [
		{
			"action": "accept",
			"src":    ["autogroup:member"],
			"dst":    ["autogroup:self"],
			"users":  ["autogroup:nonroot", "root"],
		},
	],

	"nodeAttrs": [
		// Let devices share and open Taildrive folders.
		{"target": ["*"], "attr": ["drive:share", "drive:access"]},
	],

	// Approve these automatically instead of clicking "approve" in Sphynx.
	"autoApprovers": {
		"routes":   {},
		"exitNode": [],
	},
}
`

// ManagedRule describes one rule Sphynx adds to the effective policy, so
// the UI can explain it.
type ManagedRule struct {
	Section     string `json:"section"`
	Description string `json:"description"`
	JSON        any    `json:"json"`
}

// PolicyInputs is what the managed rules depend on.
type PolicyInputs struct {
	Resilience ResilienceSettings
	// StrictIPs are tailnet addresses of devices in strict-firewall mode.
	StrictIPs []string
	// HomeIPs are Sphynx home's tailnet addresses; HomeRelay says whether
	// it acts as a UDP peer relay.
	HomeIPs   []string
	HomeRelay bool
	// RelayIPs are tailnet addresses of the other devices that may act as
	// UDP peer relays.
	RelayIPs []string
	// AdminPort is the admin page's port on Sphynx home. Like Tailscale's
	// admin console, it stays reachable from every device whatever the
	// access rules say; the admin password still protects it.
	AdminPort string
	// Chat: devices open Sphynx Chat on Sphynx home (ports 80 and 443).
	Chat bool
}

func hostPrefixes(ips []string) []string {
	var out []string
	for _, ip := range ips {
		if strings.Contains(ip, ":") {
			out = append(out, ip+"/128")
		} else {
			out = append(out, ip+"/32")
		}
	}
	sort.Strings(out)
	return out
}

// ManagedRules lists the rules Sphynx adds for its own features.
func ManagedRules(in PolicyInputs) []ManagedRule {
	var rules []ManagedRule
	var attrs []string
	var why []string
	r := in.Resilience
	if r.CacheNetmaps {
		attrs = append(attrs, "cache-network-maps")
		why = append(why, "remember peers when the server is unreachable")
	}
	if r.ClientReachability {
		attrs = append(attrs, "client-side-reachability")
		why = append(why, "keep tunnels to peers the device can still reach")
	}
	if r.ProbeUDPLifetime {
		attrs = append(attrs, "probe-udp-lifetime")
		why = append(why, "keep direct paths alive behind cellular NATs")
	}
	if r.PeerMTU {
		attrs = append(attrs, "peer-mtu-enable")
		why = append(why, "discover the packet size each path allows")
	}
	if r.AutoExitNode {
		attrs = append(attrs, "auto-exit-node", "suggest-exit-node-ui")
		why = append(why, "allow automatic exit node selection")
	}
	if len(attrs) > 0 {
		rules = append(rules, ManagedRule{
			Section:     "nodeAttrs",
			Description: "Weak-signal mode for every device: " + strings.Join(why, ", ") + ".",
			JSON:        map[string]any{"target": []string{"*"}, "attr": attrs},
		})
	}
	if len(in.StrictIPs) > 0 {
		rules = append(rules, ManagedRule{
			Section:     "nodeAttrs",
			Description: "Strict-firewall devices only use HTTPS on port 443 (all traffic relayed).",
			JSON:        map[string]any{"target": hostPrefixes(in.StrictIPs), "attr": []string{"only-tcp-443"}},
		})
	}
	if in.Chat && len(in.HomeIPs) > 0 {
		rules = append(rules, ManagedRule{
			Section:     "grants",
			Description: "Every device may open Sphynx Chat on Sphynx home.",
			JSON: map[string]any{
				"src": []string{"*"},
				"dst": hostPrefixes(in.HomeIPs),
				"ip":  []string{"tcp:80", "tcp:443"},
			},
		})
	}
	if in.AdminPort != "" && len(in.HomeIPs) > 0 {
		rules = append(rules, ManagedRule{
			Section:     "grants",
			Description: "Every device may open the admin page on Sphynx home.",
			JSON: map[string]any{
				"src": []string{"*"},
				"dst": hostPrefixes(in.HomeIPs),
				"ip":  []string{"tcp:" + in.AdminPort},
			},
		})
	}
	if in.HomeRelay && len(in.HomeIPs) > 0 {
		rules = append(rules, ManagedRule{
			Section:     "grants",
			Description: "Every device may use Sphynx home as a UDP peer relay.",
			JSON: map[string]any{
				"src": []string{"*"},
				"dst": hostPrefixes(in.HomeIPs),
				"app": map[string]any{"tailscale.com/cap/relay": []any{map[string]any{}}},
			},
		})
	}
	if len(in.RelayIPs) > 0 {
		rules = append(rules, ManagedRule{
			Section:     "grants",
			Description: "Every device may use the devices with Peer relay turned on as UDP peer relays.",
			JSON: map[string]any{
				"src": []string{"*"},
				"dst": hostPrefixes(in.RelayIPs),
				"app": map[string]any{"tailscale.com/cap/relay": []any{map[string]any{}}},
			},
		})
	}
	return rules
}

// StandardizePolicy converts HuJSON (comments, trailing commas) to JSON.
func StandardizePolicy(src string) ([]byte, error) {
	v, err := hujson.Parse([]byte(src))
	if err != nil {
		return nil, fmt.Errorf("policy is not valid HuJSON: %w", err)
	}
	v.Standardize()
	return v.Pack(), nil
}

// EffectivePolicy merges the admin's base policy with Sphynx's managed rules.
func EffectivePolicy(base string, rules []ManagedRule) (string, error) {
	if strings.TrimSpace(base) == "" {
		base = DefaultPolicy
	}
	if len(rules) == 0 {
		// Keep the admin's formatting and comments untouched.
		if _, err := StandardizePolicy(base); err != nil {
			return "", err
		}
		return base, nil
	}
	std, err := StandardizePolicy(base)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err := json.Unmarshal(std, &doc); err != nil {
		return "", fmt.Errorf("policy must be a JSON object: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	for _, r := range rules {
		var list []any
		if cur, ok := doc[r.Section]; ok && cur != nil {
			l, ok := cur.([]any)
			if !ok {
				return "", fmt.Errorf("%q must be a list", r.Section)
			}
			list = l
		}
		doc[r.Section] = append(list, r.JSON)
	}
	out, err := json.MarshalIndent(doc, "", "\t")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
