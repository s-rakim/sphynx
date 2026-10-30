package app

import (
	"time"
)

// State is Sphynx's own bookkeeping, separate from Headscale's database.
type State struct {
	// BasePolicy is the access policy as the admin wrote it (HuJSON, with
	// comments). Sphynx adds its managed rules on top before sending the
	// effective policy to Headscale.
	BasePolicy string `json:"basePolicy"`
	// Helpers are Sphynx's own tailnet nodes, one per Headscale user.
	Helpers map[string]*HelperRecord `json:"helpers"`
	// Devices holds per-device Sphynx options, keyed by Headscale node ID.
	Devices     map[string]*DeviceOptions `json:"devices"`
	WakeTargets []*WakeTarget             `json:"wakeTargets"`
	Shares      []*Share                  `json:"shares"`
}

type HelperRecord struct {
	UserID    string `json:"userId"`
	UserName  string `json:"userName"`
	SocksPort int    `json:"socksPort"`
	NodeID    string `json:"nodeId,omitempty"`
}

type DeviceOptions struct {
	// StrictFirewall makes the device only use outbound TCP 443, i.e. relay
	// everything over HTTPS. For networks that block or flag anything else.
	StrictFirewall bool `json:"strictFirewall"`
}

type WakeTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MAC  string `json:"mac"`
	// NodeID optionally links the machine being woken to its own Sphynx
	// device, so the UI can show when it comes online.
	NodeID string `json:"nodeId,omitempty"`
	// Method is "relay" (an awake device on the same LAN broadcasts the
	// packet) or "server" (this server broadcasts it itself).
	Method      string    `json:"method"`
	RelayNodeID string    `json:"relayNodeId,omitempty"`
	LastWake    time.Time `json:"lastWake,omitzero"`
	LastResult  string    `json:"lastResult,omitempty"`
}

type Share struct {
	ID   string `json:"id"`
	Name string `json:"name"` // URL slug: public at /s/<name>/
	// NodeID and Port pick the tailnet service to publish.
	NodeID  string    `json:"nodeId"`
	Port    int       `json:"port"`
	HTTPS   bool      `json:"https"` // the service itself speaks HTTPS
	Enabled bool      `json:"enabled"`
	Created time.Time `json:"created"`
}

func defaultState() State {
	return State{
		Helpers: map[string]*HelperRecord{},
		Devices: map[string]*DeviceOptions{},
	}
}
