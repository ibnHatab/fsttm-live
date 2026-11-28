package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

// TextMind is a mind without a voice: an OpenAI-compatible chat model (OpenRouter's DeepSeek Flash, …) given
// her text turns and her tools (the same declarations as the Live voice's). It says text only (MindSaid),
// no audio: for a headless Nina in the SITL (2026-11-27, the user: Gemini's free tier throttles runs of
// many scenarios; DeepSeek Flash through OpenRouter instead). It keeps the conversation itself (the Live API
// keeps it server side) and the silent context lines ahead of each turn, as Voice does.
type TextMind struct {
	URL   string // the chat completions endpoint (https://openrouter.ai/api/v1/chat/completions)
	Model string
	// Fallbacks: the models tried in order, for that call, when the one before is busy or out of quota
	// (HTTP 429, 5xx: Gemini's "high demand", a free tier's rate): google:gemini-3.8-flash,gemini-3.7-flash
	// (2026-11-28, the user: the acceptance on Gemini Flash with fallbacks on other Gemini models)
	Fallbacks []string
	KeyFile   string // the key's file: a bare key, or KEY=… lines (OPENROUTER_API_KEY)
	System    string
	Tools     []*genai.Tool
	Funcs     map[string]ToolFunc
	Out       chan<- MindEvent
	Log       func(string, map[string]any)

	mu        sync.Mutex
	key       string
	connected bool
	nextID    int
	history   []map[string]any
	ctxq      []ctxLine
	cancelled map[int]bool
}

// Connect reads the key (no session to open).
func (m *TextMind) Connect(ctx context.Context) error {
	b, err := os.ReadFile(m.KeyFile)
	if err != nil {
		return fmt.Errorf("text mind: the key: %w", err)
	}
	k := strings.TrimSpace(string(b))
	for _, l := range strings.Split(k, "\n") {
		if _, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.Contains(l, "KEY") {
			k = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	m.mu.Lock()
	m.key, m.connected = k, true
	if m.cancelled == nil {
		m.cancelled = map[int]bool{}
	}
	m.mu.Unlock()
	if m.Log != nil {
		m.Log("voice_connected", map[string]any{"model": m.Model, "text_mind": true})
	}
	return nil
}

func (m *TextMind) Connected() bool     { m.mu.Lock(); defer m.mu.Unlock(); return m.connected }
func (m *TextMind) Close()              { m.mu.Lock(); m.connected = false; m.mu.Unlock() }
func (m *TextMind) SetVoiceName(string) {}
func (m *TextMind) Context(line string) { m.ContextKey("", line) }

// ContextKey queues a silent line for her next turn (one per key: the newest).
func (m *TextMind) ContextKey(key, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if key != "" {
		for i, c := range m.ctxq {
			if c.key == key {
				m.ctxq = append(m.ctxq[:i], m.ctxq[i+1:]...)
				break
			}
		}
	}
	if line != "" {
		m.ctxq = append(m.ctxq, ctxLine{key, line})
	}
}

// Cancel stops delivering a turn's output.
func (m *TextMind) Cancel(id int) { m.mu.Lock(); m.cancelled[id] = true; m.mu.Unlock() }

// Turn sends a text turn; its events come on Out.
func (m *TextMind) Turn(ctx context.Context, text string) (int, error) {
	m.mu.Lock()
	if !m.connected {
		m.mu.Unlock()
		if err := m.Connect(ctx); err != nil {
			return 0, err
		}
		m.mu.Lock()
	}
	m.nextID++
	id := m.nextID
	user := "NOW (the local time, for you; say it only when it is asked for or needed): " + Now().Format("Monday, 2 January 2006, 15:04:05")
	if len(m.ctxq) > 0 {
		var ls []string
		for _, c := range m.ctxq {
			ls = append(ls, c.text)
		}
		user += "\nHOME (reported by the home's systems since your last turn; for you, not to announce):\n" + strings.Join(ls, "\n")
		m.ctxq = nil
	}
	m.history = append(m.history, map[string]any{"role": "user", "content": user + "\n\n" + text})
	m.mu.Unlock()
	go m.run(context.WithoutCancel(ctx), id)
	return id, nil
}

func (m *TextMind) tools() []map[string]any {
	var out []map[string]any
	for _, t := range m.Tools {
		for _, f := range t.FunctionDeclarations {
			params := any(map[string]any{"type": "object", "properties": map[string]any{}})
			if f.ParametersJsonSchema != nil {
				params = f.ParametersJsonSchema
			}
			out = append(out, map[string]any{"type": "function", "function": map[string]any{
				"name": f.Name, "description": f.Description, "parameters": params}})
		}
	}
	return out
}

func (m *TextMind) deliver(e MindEvent) {
	m.mu.Lock()
	skip := m.cancelled[e.Turn]
	m.mu.Unlock()
	if !skip && m.Out != nil {
		m.Out <- e
	}
}

// run: the model, its tool calls answered, until it says something (six rounds at most).
func (m *TextMind) run(ctx context.Context, id int) {
	defer m.deliver(MindEvent{Turn: id, Kind: MindDone})
	for round := 0; round < 6; round++ {
		m.mu.Lock()
		msgs := append([]map[string]any{{"role": "system", "content": m.System}}, m.history...)
		m.mu.Unlock()
		reply, err := m.chat(ctx, msgs)
		if err != nil {
			if m.Log != nil {
				m.Log("mind_error", map[string]any{"err": err.Error(), "text_mind": true})
			}
			m.deliver(MindEvent{Turn: id, Kind: MindSaid, Text: "Sorry, I can't think right now."})
			return
		}
		m.mu.Lock()
		m.history = append(m.history, reply)
		if len(m.history) > 40 {
			m.history = m.history[len(m.history)-40:]
		}
		m.mu.Unlock()
		calls, _ := reply["tool_calls"].([]any)
		if len(calls) == 0 {
			if s, _ := reply["content"].(string); strings.TrimSpace(s) != "" {
				m.deliver(MindEvent{Turn: id, Kind: MindSaid, Text: strings.TrimSpace(s)})
			}
			return
		}
		for _, c := range calls {
			call, _ := c.(map[string]any)
			fn, _ := call["function"].(map[string]any)
			name, _ := fn["name"].(string)
			var args map[string]any
			if s, ok := fn["arguments"].(string); ok {
				_ = json.Unmarshal([]byte(s), &args)
			}
			m.deliver(MindEvent{Turn: id, Kind: MindTool, Text: name})
			result := map[string]any{"status": "error", "text": "no such tool: " + name}
			t0 := time.Now()
			if f := m.Funcs[name]; f != nil {
				result = f(ctx, args)
			}
			if m.Log != nil { // as the Live voice logs it: her screens and the SITL's monitors read it
				m.Log("tool", map[string]any{"name": name, "args": args, "result": result, "ms": time.Since(t0).Milliseconds()})
			}
			b, _ := json.Marshal(result)
			m.mu.Lock()
			m.history = append(m.history, map[string]any{"role": "tool", "tool_call_id": call["id"], "content": string(b)})
			m.mu.Unlock()
		}
	}
}

// chat asks the model, then each fallback while the one before is busy (429, 5xx) or unreachable.
func (m *TextMind) chat(ctx context.Context, msgs []map[string]any) (map[string]any, error) {
	var err error
	for i, model := range append([]string{m.Model}, m.Fallbacks...) {
		var out map[string]any
		var busy bool
		if out, busy, err = m.chatWith(ctx, model, msgs); err == nil || !busy || ctx.Err() != nil {
			return out, err
		}
		if m.Log != nil && i < len(m.Fallbacks) {
			m.Log("mind_fallback", map[string]any{"model": model, "next": m.Fallbacks[i], "err": err.Error()})
		}
	}
	return nil, err
}

// busyStatus: the model is busy or out of its quota for now, not wrong: the next one may answer.
func busyStatus(code int) bool { return code == http.StatusTooManyRequests || code >= 500 }

func (m *TextMind) chatWith(ctx context.Context, model string, msgs []map[string]any) (map[string]any, bool, error) {
	body := map[string]any{"model": model, "messages": msgs}
	if ts := m.tools(); len(ts) > 0 {
		body["tools"] = ts
	}
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", m.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	m.mu.Lock()
	req.Header.Set("Authorization", "Bearer "+m.key)
	m.mu.Unlock()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, true, err // unreachable: the next model may be elsewhere
	}
	defer resp.Body.Close()
	var r struct {
		Choices []struct {
			Message map[string]any `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, busyStatus(resp.StatusCode), fmt.Errorf("text mind: %s: %s: %w", model, resp.Status, err)
	}
	if r.Error != nil {
		return nil, busyStatus(resp.StatusCode), fmt.Errorf("text mind: %s: %s", model, r.Error.Message)
	}
	if len(r.Choices) == 0 {
		return nil, busyStatus(resp.StatusCode), fmt.Errorf("text mind: %s: %s: no choice", model, resp.Status)
	}
	if r.Usage != nil && m.Log != nil { // the cost of a round (the SITL's metrics: tokens per turn)
		ts, _ := body["tools"].([]map[string]any)
		m.Log("mind_usage", map[string]any{"prompt_tokens": r.Usage.Prompt, "completion_tokens": r.Usage.Completion, "tools": len(ts), "model": model})
	}
	msg := r.Choices[0].Message
	out := map[string]any{"role": "assistant", "content": msg["content"]}
	if tc, ok := msg["tool_calls"]; ok && tc != nil {
		out["tool_calls"] = tc
	}
	return out, false, nil
}
