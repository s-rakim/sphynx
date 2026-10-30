package chat

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
)

// Files are uploaded as raw bytes. Message attachments are encrypted on the
// device first, so the server stores ciphertext; ?public=1 is for profile
// and group photos, which everyone on the tailnet may see.
//
// These handlers stream file bodies without holding the server lock and
// take it themselves for the database work.
func (s *Server) apiUpload(c *reqCtx) any {
	r := c.r
	length := r.ContentLength
	if length < 0 || len(r.TransferEncoding) > 0 {
		fail(411, "Content-Length is required")
	}
	if length > s.maxUploadBytes() {
		failf(413, "Files are limited to %d MB", s.maxUploadBytes()>>20)
	}
	public := c.query("public") == "1"
	if public && length > 5<<20 {
		fail(413, "Photos are limited to 5 MB")
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		fail(415, "Upload files as application/octet-stream")
	}
	uid := c.uid()
	s.withLock(func() {
		s.checkUploadQuota(uid, length, public)
		s.uploading[uid] += length
	})
	defer s.withLock(func() {
		s.uploading[uid] -= length
		if s.uploading[uid] == 0 {
			delete(s.uploading, uid)
		}
	})
	id := newID(16)
	final := s.filePath(id)
	tmp := final + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		panic(err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	// Read one byte more than announced, to catch a body that runs long.
	n, err := io.Copy(out, io.LimitReader(r.Body, length+1))
	cerr := out.Close()
	switch {
	case n > length:
		fail(400, "Body longer than Content-Length")
	case err != nil || n != length:
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(413, "Request body too large")
		}
		fail(400, "Upload was cut short")
	case cerr != nil:
		panic(cerr)
	}
	if err := os.Rename(tmp, final); err != nil {
		panic(err)
	}
	s.withLock(func() {
		s.db.run("INSERT INTO files (id, owner, size, public, created_at) VALUES (?, ?, ?, ?, ?)", id, uid, length, public, now())
	})
	ok = true
	return map[string]any{"id": id, "size": length}
}

func (s *Server) apiDownload(c *reqCtx) any {
	var f *fileRow
	s.withLock(func() { f = s.canReadFile(c.params["id"], c.uid()) })
	if f == nil {
		fail(404, "No such file")
	}
	s.serveFile(c, f, "application/octet-stream")
	return handled
}

func (s *Server) apiAvatar(c *reqCtx) any {
	var f fileRow
	var found bool
	s.withLock(func() {
		found = s.db.one(scanFile(&f), "SELECT "+fileCols+" FROM files WHERE id = ? AND public = 1", c.params["id"])
	})
	if !found {
		fail(404, "No such photo")
	}
	s.serveFile(c, &f, sniffImage(s.filePath(f.ID)))
	return handled
}

func (s *Server) serveFile(c *reqCtx, f *fileRow, contentType string) {
	fh, err := os.Open(s.filePath(f.ID))
	if err != nil {
		fail(404, "No such file")
	}
	defer fh.Close()
	h := c.w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("Content-Length", strconv.FormatInt(f.Size, 10))
	c.w.WriteHeader(http.StatusOK)
	if c.r.Method != http.MethodHead {
		_, _ = io.Copy(c.w, fh)
	}
}

// withLock runs fn under the server lock. A failed check inside fn still
// propagates (to the request), after the lock is released.
func (s *Server) withLock(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func sniffImage(path string) string {
	fh, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer fh.Close()
	head := make([]byte, 12)
	n, _ := io.ReadFull(fh, head)
	head = head[:n]
	switch {
	case len(head) >= 2 && head[0] == 0xff && head[1] == 0xd8:
		return "image/jpeg"
	case len(head) >= 4 && string(head[:4]) == "\x89PNG":
		return "image/png"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image/webp"
	case len(head) >= 3 && string(head[:3]) == "GIF":
		return "image/gif"
	}
	return "application/octet-stream"
}
