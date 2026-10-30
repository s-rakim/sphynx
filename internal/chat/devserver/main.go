// Command devserver runs Sphynx Chat on loopback in dev mode, where any
// test device can be picked by name. The API contract tests in ../test use
// it; it is not part of Sphynx itself.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/s-rakim/sphynx/internal/chat"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v, err := strconv.Atoi(env(k, strconv.Itoa(def)))
	if err != nil {
		log.Fatalf("%s must be a number", k)
	}
	return v
}

func main() {
	host := env("CHAT_HOST", "127.0.0.1")
	if host != "localhost" && !chat.IsLoopback(host) {
		log.Fatal("dev mode only listens on loopback (set CHAT_HOST=127.0.0.1)")
	}
	cfg := chat.Config{
		DataDir:        env("CHAT_DATA", "./data"),
		MaxUploadBytes: int64(envInt("CHAT_MAX_UPLOAD_MB", 2048)) << 20,
		StatusHours:    envInt("CHAT_STATUS_HOURS", 24),
		EditWindow:     time.Duration(envInt("CHAT_EDIT_WINDOW_S", 15*60)) * time.Second,
		DeleteWindow:   time.Duration(envInt("CHAT_DELETE_WINDOW_S", 2*24*3600)) * time.Second,
		SweepInterval:  time.Duration(envInt("CHAT_SWEEP_INTERVAL_MS", 15000)) * time.Millisecond,
	}
	// CHAT_DEV_DEVICES lists made-up tailnet devices, "name:os:owner,...",
	// which show up as contacts before they open the chat.
	devices := chat.ParseDevDirectory(os.Getenv("CHAT_DEV_DEVICES"))
	var dir chat.Directory
	if len(devices) > 0 {
		dir = devices
	}
	srv, err := chat.New(cfg, dir)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Run(ctx)
	addr := net.JoinHostPort(host, env("CHAT_PORT", "8443"))
	hs := &http.Server{Addr: addr, Handler: srv.Handler(chat.DevResolver{Devices: devices}), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		_ = hs.Close()
	}()
	log.Printf("Sphynx Chat (dev mode) on http://%s/dev/login", addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	_ = srv.Close()
}
