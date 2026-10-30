// Command devserver runs Sphynx Chat on loopback in dev mode, where any
// test device can be picked by name. The API contract tests in ../test use
// it; it is not part of Sphynx itself.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler(chat.DevResolver{Devices: devices}))
	devRadio(srv, mux)
	addr := net.JoinHostPort(host, env("CHAT_PORT", "8443"))
	hs := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 30 * time.Second}
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

// devRadio adds radio contacts from CHAT_DEV_RADIO, "Name=link:address,...",
// whose messages go to the CHAT_DEV_RADIO_TO accounts. POST
// /dev/radio/receive plays a message from one ({"key", "text"}), and GET
// /dev/radio/sent lists what the chat sent over the "radio" (texts
// containing "fail" are not acknowledged).
func devRadio(srv *chat.Server, mux *http.ServeMux) {
	spec := os.Getenv("CHAT_DEV_RADIO")
	if spec == "" {
		return
	}
	var contacts []chat.RadioContact
	for _, item := range strings.Split(spec, ",") {
		name, key, ok := strings.Cut(item, "=")
		link, addr, ok2 := strings.Cut(key, ":")
		if ok && ok2 {
			maxText := 520
			if link == "aprs" {
				maxText = 240
			}
			contacts = append(contacts, chat.RadioContact{Key: key, Name: name, Detail: link + " " + addr, Link: link, MaxText: maxText})
		}
	}
	var mu sync.Mutex
	sent := []map[string]string{}
	deliverTo := strings.Split(os.Getenv("CHAT_DEV_RADIO_TO"), ",")
	srv.SetRadio(contacts, func(ctx context.Context, key, text string) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, map[string]string{"key": key, "text": text})
		if strings.Contains(text, "fail") {
			return errors.New("no acknowledgement")
		}
		return nil
	})
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /dev/radio/receive", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Key, Text string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		names, err := srv.RadioReceived(req.Key, req.Text, deliverTo)
		if err != nil {
			reply(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, map[string]any{"names": names})
	})
	mux.HandleFunc("GET /dev/radio/sent", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reply(w, http.StatusOK, map[string]any{"sent": sent})
	})
}
