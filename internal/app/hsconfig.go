package app

import (
	"encoding/json"
	"path/filepath"
)

// Paths inside the container. Everything persistent lives under DataDir.
type Paths struct {
	DataDir       string // /data
	HeadscaleBin  string
	TailscaleBin  string
	TailscaledBin string
	TailcatBin    string // optional; enables Sphynx Link
	RunDir        string // sockets
}

func (p Paths) HeadscaleDir() string    { return filepath.Join(p.DataDir, "headscale") }
func (p Paths) SphynxDir() string       { return filepath.Join(p.DataDir, "sphynx") }
func (p Paths) TailscaleDir() string    { return filepath.Join(p.DataDir, "tailscale") }
func (p Paths) HeadscaleConfig() string { return filepath.Join(p.HeadscaleDir(), "config.yaml") }
func (p Paths) ExtraRecords() string    { return filepath.Join(p.HeadscaleDir(), "extra-records.json") }
func (p Paths) HeadscaleSocket() string { return filepath.Join(p.RunDir, "headscale.sock") }
func (p Paths) ACMECache() string       { return filepath.Join(p.HeadscaleDir(), "cache") }

// Headscale only listens on loopback. Sphynx's front server owns the public
// ports, terminates TLS, serves public shares and proxies everything else
// (control protocol, DERP, registration pages) to Headscale.
const (
	headscaleAddr = "127.0.0.1:18080"
	headscaleURL  = "http://" + headscaleAddr
)

// HeadscaleConfig renders Headscale's config. JSON is valid YAML, so the
// result can be written straight to config.yaml.
func (s *Settings) HeadscaleConfig(p Paths, oidcSecret string) ([]byte, error) {
	hd := p.HeadscaleDir()

	// Public relays come from a local copy (see derpmap.go), never a URL.
	derpPaths := []string{}
	if s.DERP.UsePublic {
		derpPaths = append(derpPaths, p.PublicDERPMap())
	}
	derpServer := map[string]any{
		"enabled":                                s.DERP.Embedded,
		"region_id":                              999,
		"region_code":                            "sphynx",
		"region_name":                            "Sphynx",
		"verify_clients":                         true,
		"stun_listen_addr":                       "0.0.0.0:3478",
		"private_key_path":                       filepath.Join(hd, "derp_server_private.key"),
		"automatically_add_embedded_derp_region": true,
	}
	if s.DERP.IPv4 != "" {
		derpServer["ipv4"] = s.DERP.IPv4
	}
	if s.DERP.IPv6 != "" {
		derpServer["ipv6"] = s.DERP.IPv6
	}

	split := map[string][]string{}
	for k, v := range s.DNS.SplitDNS {
		split[k] = v
	}
	nameservers := s.DNS.Nameservers
	if nameservers == nil {
		nameservers = []string{}
	}
	search := s.DNS.SearchDomains
	if search == nil {
		search = []string{}
	}

	cfg := map[string]any{
		"server_url":          s.ServerURL,
		"listen_addr":         headscaleAddr,
		"metrics_listen_addr": "127.0.0.1:9090",
		"grpc_listen_addr":    "127.0.0.1:50443",
		"grpc_allow_insecure": false,
		// The front server forwards real client addresses.
		"trusted_proxies": []string{"127.0.0.1/32", "::1/128"},
		"noise": map[string]any{
			"private_key_path": filepath.Join(hd, "noise_private.key"),
		},
		"prefixes": map[string]any{
			"v4":         s.Prefixes.V4,
			"v6":         s.Prefixes.V6,
			"allocation": s.Prefixes.Allocation,
		},
		"derp": map[string]any{
			"server":              derpServer,
			"urls":                []string{},
			"paths":               derpPaths,
			"auto_update_enabled": true,
			"update_frequency":    "1h",
		},
		"disable_check_updates": true,
		"node": map[string]any{
			"expiry": s.Nodes.Expiry,
			"ephemeral": map[string]any{
				"inactivity_timeout": s.Nodes.EphemeralTimeout,
			},
		},
		"database": map[string]any{
			"type": "sqlite",
			"sqlite": map[string]any{
				"path":               filepath.Join(hd, "db.sqlite"),
				"write_ahead_log":    true,
				"wal_autocheckpoint": 1000,
			},
		},
		"log": map[string]any{
			"level":  s.LogLevel,
			"format": "text",
		},
		// The policy lives in Headscale's database so the UI can edit it.
		"policy": map[string]any{
			"mode": "database",
		},
		"dns": map[string]any{
			"magic_dns":          s.DNS.MagicDNS,
			"base_domain":        s.DNS.BaseDomain,
			"override_local_dns": s.DNS.OverrideLocalDNS,
			"nameservers": map[string]any{
				"global": nameservers,
				"split":  split,
			},
			"search_domains":     search,
			"extra_records_path": p.ExtraRecords(),
		},
		"unix_socket":            p.HeadscaleSocket(),
		"unix_socket_permission": "0770",
		// Never ship client logs to Tailscale Inc.
		"logtail": map[string]any{
			"enabled": false,
		},
		"taildrop": map[string]any{
			"enabled": s.Features.Taildrop,
		},
		"auto_update": map[string]any{
			"enabled": s.Features.AutoUpdate,
		},
	}
	if s.OIDC.Enabled {
		scopes := s.OIDC.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "profile", "email"}
		}
		oidc := map[string]any{
			"only_start_if_oidc_is_available": false,
			"issuer":                          s.OIDC.Issuer,
			"client_id":                       s.OIDC.ClientID,
			"client_secret":                   oidcSecret,
			"scope":                           scopes,
			"pkce": map[string]any{
				"enabled": s.OIDC.PKCE,
				"method":  "S256",
			},
		}
		if len(s.OIDC.AllowedDomains) > 0 {
			oidc["allowed_domains"] = s.OIDC.AllowedDomains
		}
		if len(s.OIDC.AllowedUsers) > 0 {
			oidc["allowed_users"] = s.OIDC.AllowedUsers
		}
		if len(s.OIDC.AllowedGroups) > 0 {
			oidc["allowed_groups"] = s.OIDC.AllowedGroups
		}
		cfg["oidc"] = oidc
	}
	return json.MarshalIndent(cfg, "", "  ")
}
