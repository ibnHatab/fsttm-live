package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCORS: an allowed origin gets its header and a preflight answer; another gets neither.
func TestCORS(t *testing.T) {
	w := &Web{AllowOrigin: []string{"http://homeassistant.local:8123"}}
	h := w.cors(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(200) }))
	for _, c := range []struct {
		method, origin string
		code           int
		allowed        bool
	}{
		{"GET", "http://homeassistant.local:8123", 200, true},
		{"OPTIONS", "http://homeassistant.local:8123", 204, true},
		{"GET", "http://evil.example", 200, false},
	} {
		req := httptest.NewRequest(c.method, "/api/state", nil)
		req.Header.Set("Origin", c.origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("Access-Control-Allow-Origin") == c.origin
		if rec.Code != c.code || got != c.allowed {
			t.Fatalf("%s from %s: %d, allowed %v", c.method, c.origin, rec.Code, got)
		}
	}
}
