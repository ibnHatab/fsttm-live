package web

import (
	"net/http/httptest"
	"testing"
)

// From outside (through Tailscale serve), "/" is her own screen; a local visit is this panel.
func TestRemoteRoot(t *testing.T) {
	w := &Web{Remote: "/nina/shell.html"}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "100.64.0.1")
	if !proxied(r) || w.Remote == "" {
		t.Fatal("a proxied request not seen")
	}
	if proxied(httptest.NewRequest("GET", "/", nil)) {
		t.Fatal("a local request taken for a proxied one")
	}
}
