package live

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The model busy (429, Gemini's "high demand"): the turn goes to the next model, and the answer is said.
// A model that is wrong (400) is not passed over: its error is hers.
func TestTheTextMindFallsBackWhenItsModelIsBusy(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		asked = append(asked, b.Model)
		mu.Unlock()
		switch b.Model {
		case "busy":
			w.WriteHeader(http.StatusTooManyRequests) // Gemini's own shape: a list, not an object
			_, _ = w.Write([]byte(`[{"error":{"code":429,"message":"RESOURCE_EXHAUSTED"}}]`))
		case "hangs":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		case "wrong":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello from ` + b.Model + `"}}]}`))
		}
	}))
	defer srv.Close()
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("GEMINI_API_KEY=test\n"), 0o600)
	var logged []string
	m := &TextMind{URL: srv.URL, Model: "busy", Fallbacks: []string{"ok"}, KeyFile: key,
		Log: func(k string, _ map[string]any) { logged = append(logged, k) }}
	_ = m.Connect(context.Background())
	out, err := m.chat(context.Background(), []map[string]any{{"role": "user", "content": "hi"}})
	if err != nil || out["content"] != "hello from ok" {
		t.Fatalf("busy → next: %v %v", out, err)
	}
	if len(asked) != 2 || asked[0] != "busy" || asked[1] != "ok" || logged[len(logged)-1] != "mind_fallback" {
		t.Fatalf("asked %v, logged %v", asked, logged)
	}
	asked = nil // a model that hangs is left for the next one, while there is a next one
	TryWait = 100 * time.Millisecond
	m.Model = "hangs"
	if out, err := m.chat(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}); err != nil || out["content"] != "hello from ok" {
		t.Fatalf("hangs → next: %v %v (asked %v)", out, err, asked)
	}
	asked = nil
	m.Model = "wrong"
	if _, err := m.chat(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}); err == nil || len(asked) != 1 {
		t.Fatalf("a wrong request is not passed over: %v, asked %v", err, asked)
	}
}
