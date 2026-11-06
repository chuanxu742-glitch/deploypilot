package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestAcceptEncodingZeroQuality(t *testing.T) {
	for _, quality := range []string{"0", "0.0", "0.00", "0.000"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+"_q_"+quality, func(t *testing.T) {
				js := strings.Repeat("console.log('local fixture');\n", 200)
				assets := fstest.MapFS{"assets/index-fixture.js": {Data: []byte(js)}}
				s := New(Options{Assets: assets})
				req := httptest.NewRequest(method, "/assets/index-fixture.js", nil)
				req.Header.Set("Accept-Encoding", "gzip;q="+quality+", identity;q=1")
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, req)
				t.Logf("%s Accept-Encoding=%q status=%d Content-Encoding=%q bytes=%d", method, req.Header.Get("Accept-Encoding"), rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.Len())
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d", rec.Code)
				}
				if rec.Header().Get("Content-Encoding") != "" {
					t.Errorf("gzip quality zero must return unencoded representation, headers=%v", rec.Header())
				}
				if method == http.MethodGet && rec.Body.String() != js {
					t.Errorf("GET response is not the accepted identity representation")
				}
				if method == http.MethodHead && rec.Body.Len() != 0 {
					t.Errorf("HEAD has body")
				}
			})
		}
	}
}

func TestPositiveQualityAndRangeControls(t *testing.T) {
	js := strings.Repeat("console.log('local fixture');\n", 200)
	for _, encoding := range []string{"gzip", "gzip;q=1", "gzip;q=1.000", "gzip;q=0.001", "br, gzip;q=0.5", "GZIP; q=0.75"} {
		t.Run(encoding, func(t *testing.T) {
			assets := fstest.MapFS{"assets/index-fixture.js": {Data: []byte(js)}}
			s := New(Options{Assets: assets})
			req := httptest.NewRequest(http.MethodGet, "/assets/index-fixture.js", nil)
			req.Header.Set("Accept-Encoding", encoding)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Header().Get("Content-Encoding") != "gzip" {
				t.Fatalf("expected gzip: %v", rec.Header())
			}
			zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			_ = zr.Close()
			if string(decoded) != js {
				t.Fatal("decoded body differs")
			}
			if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
				t.Fatal("wrong content length")
			}
			if rec.Header().Get("Cache-Control") != immutableCache || rec.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatal("cache headers changed")
			}
			req.Header.Set("Range", "bytes=0-9")
			rec = httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != js[:10] {
				t.Fatalf("range changed: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
			}
		})
	}
}
