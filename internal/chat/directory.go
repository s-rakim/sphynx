package chat

import (
	"context"
	"time"
)

// The directory is Sphynx's list of devices on the tailnet. It is cached
// and refreshed outside the server lock, because reading it can mean a
// call to Headscale.

func (s *Server) refreshDirectory(ctx context.Context, maxAge time.Duration) {
	if s.dir == nil {
		return
	}
	s.dirMu.Lock()
	fresh := time.Since(s.dirAt) < maxAge
	s.dirMu.Unlock()
	if fresh {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := s.dir.Devices(ctx)
	if err != nil {
		return
	}
	s.dirMu.Lock()
	s.dirCache, s.dirAt = list, time.Now()
	s.dirMu.Unlock()
}

// sawDevice adds a device the resolver just identified, which is on the
// tailnet by definition, if the cached directory predates it.
func (s *Server) sawDevice(id Identity) {
	if s.dir == nil {
		return
	}
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	for _, d := range s.dirCache {
		if d.ID == id.ID {
			return
		}
	}
	s.dirCache = append(s.dirCache, id)
}

func (s *Server) directorySnapshot() []Identity {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	return append([]Identity(nil), s.dirCache...)
}

func (s *Server) directoryEntry(id string) (Identity, bool) {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	for _, d := range s.dirCache {
		if d.ID == id {
			return d, true
		}
	}
	return Identity{}, false
}

// onNetwork reports whether an account's device is still on the tailnet.
// Without a directory (dev mode) everyone is.
func (s *Server) onNetwork(id string) bool {
	if s.dir == nil {
		return true
	}
	_, ok := s.directoryEntry(id)
	return ok
}

// refreshFromDirectory keeps an account's tailnet name, owner and OS in
// step with Sphynx, e.g. after the admin renamed the device.
func (s *Server) refreshFromDirectory(u *userRow) {
	d, ok := s.directoryEntry(u.ID)
	if !ok {
		return
	}
	name := truncate(d.Name, 60)
	owner, osName := u.Owner, u.OS
	if d.Owner != "" {
		owner = d.Owner
	}
	if d.OS != "" {
		osName = d.OS
	}
	deviceName := u.DeviceName
	if name != "" {
		deviceName = name
	}
	rename := !u.CustomName && name != "" && u.Name != name
	if !rename && u.DeviceName == deviceName && u.Owner == owner && u.OS == osName {
		return
	}
	if rename {
		u.Name = name
	}
	u.DeviceName, u.Owner, u.OS = deviceName, owner, osName
	s.db.run("UPDATE users SET name = ?, device_name = ?, owner = ?, os = ? WHERE id = ?", u.Name, u.DeviceName, u.Owner, u.OS, u.ID)
}
