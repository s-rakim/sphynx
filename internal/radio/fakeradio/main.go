// Command fakeradio runs pretend radios, for trying Sphynx's radio mode
// without hardware: a Meshtastic radio on the network (port 4403) and an
// APRS-IS server (port 14580), plus a small web API that plays the radios
// in the field. It is a developer tool, not part of Sphynx.
//
//	go run ./internal/radio/fakeradio -nodes '!a1b2c3d4=Field radio' -calls 5Z4ABC-7
//
// On Sphynx's Radio page, use this computer's address as the Meshtastic
// radio (like 192.168.1.20:4403) and APRS-IS server (192.168.1.20:14580,
// passcode 12345), add the radios as contacts, then:
//
//	curl -d '{"from":"!a1b2c3d4","text":"status"}' localhost:8090/meshtastic
//	curl -d '{"from":"5Z4ABC-7","text":"ping"}' localhost:8090/aprs
//	curl localhost:8090/sent
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/s-rakim/sphynx/internal/radio"
	"github.com/s-rakim/sphynx/internal/radio/radiotest"
)

func main() {
	meshAddr := flag.String("meshtastic", ":4403", "where the pretend Meshtastic radio listens")
	aprsAddr := flag.String("aprs", ":14580", "where the pretend APRS-IS server listens")
	httpAddr := flag.String("http", "127.0.0.1:8090", "where the web API listens")
	passcode := flag.String("passcode", "12345", "the APRS-IS passcode accepted as verified")
	nodes := flag.String("nodes", "!a1b2c3d4=Field radio", "Meshtastic radios in the field, as id=name,...")
	calls := flag.String("calls", "5Z4ABC-7", "APRS stations in the field that acknowledge messages, comma separated")
	flag.Parse()

	mesh, err := radiotest.NewMeshtastic(*meshAddr, 0x0badcafe, "Home radio")
	if err != nil {
		log.Fatal(err)
	}
	for _, n := range strings.Split(*nodes, ",") {
		id, name, _ := strings.Cut(strings.TrimSpace(n), "=")
		num, ok := radio.ParseNodeID(id)
		if !ok {
			log.Fatalf("%q isn't a Meshtastic node id like !a1b2c3d4", id)
		}
		mesh.AddNode(radiotest.MeshNode{Num: num, LongName: name})
	}
	aprs, err := radiotest.NewAPRSIS(*aprsAddr, *passcode)
	if err != nil {
		log.Fatal(err)
	}
	for _, c := range strings.Split(*calls, ",") {
		if c = strings.TrimSpace(c); c != "" {
			aprs.Acking(c, true)
		}
	}

	var msgID atomic.Int64
	reply := func(w http.ResponseWriter, err error, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			v = map[string]string{"error": err.Error()}
		}
		_ = json.NewEncoder(w).Encode(v)
	}
	type in struct {
		From string  `json:"from"`
		Text string  `json:"text"`
		Hops int     `json:"hops"`
		SNR  float32 `json:"snr"`
	}
	read := func(r *http.Request) (in, bool) {
		var m in
		err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(&m)
		return m, err == nil && m.From != "" && m.Text != ""
	}
	http.HandleFunc("POST /meshtastic", func(w http.ResponseWriter, r *http.Request) {
		m, ok := read(r)
		num, ok2 := radio.ParseNodeID(m.From)
		if !ok || !ok2 {
			http.Error(w, `send {"from": "!a1b2c3d4", "text": "..."}`, http.StatusBadRequest)
			return
		}
		reply(w, mesh.Inject(radiotest.MeshIn{From: num, Text: m.Text, Hops: m.Hops, SNR: m.SNR, Private: true}), map[string]bool{"ok": true})
	})
	http.HandleFunc("POST /aprs", func(w http.ResponseWriter, r *http.Request) {
		m, ok := read(r)
		if !ok {
			http.Error(w, `send {"from": "5Z4ABC-7", "text": "..."}`, http.StatusBadRequest)
			return
		}
		id := strconv.FormatInt(msgID.Add(1), 10)
		reply(w, aprs.Inject(strings.ToUpper(m.From), aprs.LoggedIn(), m.Text, id), map[string]bool{"ok": true})
	})
	http.HandleFunc("GET /sent", func(w http.ResponseWriter, r *http.Request) {
		type sent struct {
			To   string    `json:"to"`
			Text string    `json:"text"`
			At   time.Time `json:"at"`
		}
		out := []sent{}
		for _, s := range mesh.Sent() {
			out = append(out, sent{radio.NodeID(s.To), s.Text, s.At})
		}
		reply(w, nil, map[string]any{"meshtastic": out, "aprs": append([]string{}, aprs.Lines()...)})
	})
	http.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, nil, map[string]any{"meshtasticConnected": mesh.Connected(), "aprsLoggedIn": aprs.LoggedIn()})
	})
	log.Printf("pretend Meshtastic radio on %s, APRS-IS server on %s (passcode %s), web API on http://%s/", mesh.Addr(), aprs.Addr(), *passcode, *httpAddr)
	srv := &http.Server{Addr: *httpAddr, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
