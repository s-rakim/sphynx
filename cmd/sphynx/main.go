// Command sphynx runs the Sphynx server: a self-hosted Tailscale control
// plane (Headscale) with a friendly web UI, built-in exit node and
// Wake-on-LAN.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/s-rakim/sphynx/internal/app"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		dataDir    = flag.String("data", envOr("SPHYNX_DATA", "/data"), "directory for all persistent state")
		runDir     = flag.String("run", envOr("SPHYNX_RUN", "/run/sphynx"), "directory for unix sockets")
		adminAddr  = flag.String("admin", envOr("SPHYNX_ADMIN_ADDR", ":3000"), "address of the admin web UI")
		httpAddr   = flag.String("http", envOr("SPHYNX_HTTP_ADDR", ":8080"), "plain HTTP address for devices")
		tlsAddr    = flag.String("https", envOr("SPHYNX_HTTPS_ADDR", ":443"), "HTTPS address for devices (when TLS is on)")
		acmeAddr   = flag.String("acme", envOr("SPHYNX_ACME_ADDR", ":80"), "HTTP address for Let's Encrypt challenges and redirects")
		headscale  = flag.String("headscale", envOr("SPHYNX_HEADSCALE_BIN", "/usr/local/bin/headscale"), "path to the headscale binary")
		tailscale  = flag.String("tailscale", envOr("SPHYNX_TAILSCALE_BIN", "/usr/local/bin/tailscale"), "path to the tailscale CLI")
		tailscaled = flag.String("tailscaled", envOr("SPHYNX_TAILSCALED_BIN", "/usr/local/bin/tailscaled"), "path to tailscaled")
		tailcat    = flag.String("tailcat", envOr("SPHYNX_TAILCAT_BIN", "/usr/local/bin/tailcat"), "path to the tailcat CLI (Sphynx Link); empty disables it")
		homePort   = flag.Int("home-wireguard-port", 41642, "UDP port of Sphynx home's WireGuard (publish it for direct connections; not 41641, which the Tailscale app on the same PC uses)")
		chatAddr   = flag.String("chat", envOr("SPHYNX_CHAT_ADDR", ":3001"), "address where this PC opens Sphynx Chat as Sphynx home")
	)
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	a, err := app.New(app.Config{
		Paths: app.Paths{
			DataDir:       *dataDir,
			RunDir:        *runDir,
			HeadscaleBin:  *headscale,
			TailscaleBin:  *tailscale,
			TailscaledBin: *tailscaled,
			TailcatBin:    *tailcat,
		},
		Version:       version,
		AdminAddr:     *adminAddr,
		HTTPAddr:      *httpAddr,
		TLSAddr:       *tlsAddr,
		ACMEAddr:      *acmeAddr,
		HomeWGPort:    *homePort,
		ChatLocalAddr: *chatAddr,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
