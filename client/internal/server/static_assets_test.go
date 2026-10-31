package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticAssetsAreCachedAndCompressed(t *testing.T) {
	js := strings.Repeat("console.log('deploypilot');\n", 200)
	srv := New(Options{Version: "t", Assets: fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>x</title>" + strings.Repeat(" ", 2048))},
		"assets/index-abc.js":   {Data: []byte(js)},
		"assets/font-abc.woff2": {Data: bytes.Repeat([]byte{1, 2, 3}, 1000)},
	}})
	get := func(path, encoding string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if encoding != "" {
			req.Header.Set("Accept-Encoding", encoding)
		}
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	rec := get("/assets/index-abc.js", "gzip, deflate, br")
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("hashed JS: code=%d headers=%v", rec.Code, rec.Header())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != js {
		t.Fatal("gzipped body does not round-trip")
	}

	if rec := get("/assets/index-abc.js", ""); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != js {
		t.Fatalf("client without gzip got %v", rec.Header())
	}
	if rec := get("/assets/index-abc.js", "gzip;q=0"); rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("q=0 must disable gzip")
	}
	if rec := get("/assets/index-abc.js", "gzip", "Range", "bytes=0-9"); rec.Code != 206 || rec.Body.Len() != 10 {
		t.Fatalf("range request: code=%d len=%d", rec.Code, rec.Body.Len())
	}
	if rec := get("/assets/font-abc.woff2", "gzip"); rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("font: %v", rec.Header())
	}
	for _, path := range []string{"/", "/some/client/route"} {
		rec := get(path, "gzip")
		if rec.Code != 200 || strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || rec.Header().Get("Cache-Control") == "" {
			t.Fatalf("%s must be revalidated: code=%d %v", path, rec.Code, rec.Header())
		}
	}
}
