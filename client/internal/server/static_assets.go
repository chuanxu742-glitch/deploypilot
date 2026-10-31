package server

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// Vite puts content-hashed files under assets/, so a name never changes
// meaning: browsers may keep them for a year. index.html is revalidated on
// every visit so an upgraded binary is picked up immediately. Embedded files
// have no modification time, so without these headers browsers re-download
// the whole bundle (about 1 MB) on every page load.
const immutableCache = "public, max-age=31536000, immutable"

// compressible lists the text types worth gzipping; fonts and images are
// already compressed.
var compressible = map[string]bool{".js": true, ".css": true, ".html": true, ".svg": true, ".json": true, ".txt": true, ".map": true}

// gzipCache holds compressed copies of embedded files, computed once.
type gzipCache struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (c *gzipCache) get(assets fs.FS, name string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if data, ok := c.files[name]; ok {
		return data, data != nil
	}
	if c.files == nil {
		c.files = make(map[string][]byte)
	}
	raw, err := fs.ReadFile(assets, name)
	if err != nil || len(raw) < 1024 {
		c.files[name] = nil
		return nil, false
	}
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = w.Write(raw)
	_ = w.Close()
	if buf.Len() >= len(raw) {
		c.files[name] = nil
		return nil, false
	}
	c.files[name] = buf.Bytes()
	return c.files[name], true
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			return strings.TrimSpace(strings.ReplaceAll(params, " ", "")) != "q=0"
		}
	}
	return false
}

// serveAsset writes an existing embedded file with cache headers, gzipped
// when the client accepts it. Range requests get the plain file.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string, fileServer http.Handler) {
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", immutableCache)
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	ext := path.Ext(name)
	if !compressible[ext] {
		fileServer.ServeHTTP(w, r)
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if r.Header.Get("Range") != "" || !acceptsGzip(r) {
		fileServer.ServeHTTP(w, r)
		return
	}
	data, ok := s.gzipped.get(s.assets, name)
	if !ok {
		fileServer.ServeHTTP(w, r)
		return
	}
	if ctype := mime.TypeByExtension(ext); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
