package app

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Headscale refuses to start when it can't download its relay (DERP) map,
// which would take the whole network down whenever this PC boots before its
// internet connection is up. So Sphynx downloads Tailscale's public relay
// map itself, keeps a copy in the data volume and gives Headscale the file.

const publicDERPMapURL = "https://controlplane.tailscale.com/derpmap/default"

// fallbackDERPMap is used until the first download succeeds. It is the
// snapshot of Tailscale's public relays that ships inside the Tailscale
// client (tailscale v1.104.4, net/dnsfallback/dns-fallback-servers.json).
//
//go:embed derpmap-fallback.json
var fallbackDERPMap []byte

func (p Paths) PublicDERPMap() string { return filepath.Join(p.SphynxDir(), "derpmap-public.yaml") }

// ensurePublicDERPMap makes sure Headscale has a relay map file to read.
func (a *App) ensurePublicDERPMap() error {
	if _, err := os.Stat(a.cfg.Paths.PublicDERPMap()); err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := a.downloadPublicDERPMap(ctx)
	if err == nil {
		return nil
	}
	a.Logf("couldn't download Tailscale's relay map (%v); using the built-in copy until it works", err)
	y, err := derpMapYAML(fallbackDERPMap)
	if err != nil {
		return err
	}
	return writeFileAtomic(a.cfg.Paths.PublicDERPMap(), y, 0o644)
}

func (a *App) downloadPublicDERPMap(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicDERPMapURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", publicDERPMapURL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	y, err := derpMapYAML(b)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(a.cfg.Paths.PublicDERPMap()); err == nil && bytes.Equal(old, y) {
		return nil
	}
	return writeFileAtomic(a.cfg.Paths.PublicDERPMap(), y, 0o644)
}

// refreshPublicDERPMap keeps the copy current. Headscale re-reads the file
// every hour (derp.update_frequency).
func (a *App) refreshPublicDERPMap(ctx context.Context) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if !a.settings.Get().DERP.UsePublic {
			wait = time.Hour
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := a.downloadPublicDERPMap(cctx)
		cancel()
		if err != nil {
			// Retry soon while offline, but don't spam the log.
			wait = min(wait*2, time.Hour)
			continue
		}
		wait = 12 * time.Hour
	}
}

// derpMapYAML converts a DERP map from the JSON that Tailscale serves to
// the YAML that Headscale reads from files. Headscale decodes the file with
// yaml.v3 into tailcfg.DERPMap, so keys must be the lowercased Go field
// names and region IDs must be plain integers.
func derpMapYAML(j []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("relay map: %w", err)
	}
	regions, _ := v["Regions"].(map[string]any)
	if len(regions) == 0 {
		return nil, errors.New("relay map has no regions")
	}
	var buf bytes.Buffer
	buf.WriteString("# Tailscale's public relays, kept up to date by Sphynx.\n")
	writeYAML(&buf, v, 0)
	return buf.Bytes(), nil
}

func writeYAML(buf *bytes.Buffer, v any, indent int) {
	pad := strings.Repeat("  ", indent)
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			key := strings.ToLower(k)
			if _, err := strconv.Atoi(k); err != nil {
				key = strconv.Quote(key)
			}
			switch val := x[k].(type) {
			case map[string]any:
				if len(val) == 0 {
					fmt.Fprintf(buf, "%s%s: {}\n", pad, key)
					continue
				}
				fmt.Fprintf(buf, "%s%s:\n", pad, key)
				writeYAML(buf, val, indent+1)
			case []any:
				if len(val) == 0 {
					fmt.Fprintf(buf, "%s%s: []\n", pad, key)
					continue
				}
				fmt.Fprintf(buf, "%s%s:\n", pad, key)
				writeYAML(buf, val, indent+1)
			default:
				fmt.Fprintf(buf, "%s%s: %s\n", pad, key, yamlScalar(val))
			}
		}
	case []any:
		for _, item := range x {
			switch val := item.(type) {
			case map[string]any:
				// "- " followed by the map, indented one level.
				var inner bytes.Buffer
				writeYAML(&inner, val, indent+1)
				s := inner.String()
				s = strings.TrimPrefix(s, strings.Repeat("  ", indent+1))
				fmt.Fprintf(buf, "%s- %s", pad, s)
			default:
				fmt.Fprintf(buf, "%s- %s\n", pad, yamlScalar(val))
			}
		}
	}
}

func yamlScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case json.Number:
		return x.String()
	case string:
		return strconv.Quote(x)
	}
	return strconv.Quote(fmt.Sprint(v))
}
