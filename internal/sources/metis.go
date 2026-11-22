// Package sources connects Nina to SOURCES, the systems she draws on. Metis (metis-home, the
// home's rule engine) is the first:
//
//	its tool port (127.0.0.1:8768, FSTTM's hometools protocol): one tool per package, a call
//	answered with the rules' outcome: the voice session's tools
//	the stream door (127.0.0.1:8766, opened here; metis-home connects to it): its REASONING
//	(outcomes, things it noticed, surprises) and ENVIRONMENT lines
//
// What comes through the door goes either into the voice session's context (silently: she
// knows it, says nothing) or to the floor as a NOTE with a level, the narration port's
// quantisation. A line may carry its own "level" (info, important, urgent, or "context");
// otherwise the kind of the line decides (Levels: result, noticed, surprise, error, env).
package sources

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"time"

	"github.com/ibnHatab/fsttm-live/internal/live"
	"github.com/ibnHatab/fsttm/go/pkg/hometools"
	"google.golang.org/genai"
)

type Metis struct {
	ToolAddr string            `yaml:"tools"`  // metis-home's tool port
	DoorAddr string            `yaml:"door"`   // the stream door we open
	Levels   map[string]string `yaml:"levels"` // line kind → info | important | urgent | context | drop
	Wait     time.Duration     `yaml:"wait"`   // how long to wait for its tools at start

	client *hometools.Client
}

// Line is one thing SOURCES said, routed: Level "context" goes to the voice session silently.
type Line struct {
	Kind  string // result, noticed, surprise, error, env, other
	Level string
	Text  string
}

// Tools asks metis-home for its tools (waiting up to Wait for it to come up) and makes them
// the voice session's: the OpenAI schemas pass through as JSON Schema.
func (m *Metis) Tools(ctx context.Context, log func(string, map[string]any)) ([]*genai.Tool, map[string]live.ToolFunc) {
	m.client = &hometools.Client{Addr: m.ToolAddr, Retry: time.Second, Timeout: 8 * time.Second}
	var raw []json.RawMessage
	deadline := time.Now().Add(m.Wait)
	for {
		if raw = m.client.Tools(); raw != nil || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	var decls []*genai.FunctionDeclaration
	funcs := map[string]live.ToolFunc{}
	for _, r := range raw {
		var t hometools.Tool
		if json.Unmarshal(r, &t) != nil || t.Function.Name == "" {
			continue
		}
		name := t.Function.Name
		decls = append(decls, &genai.FunctionDeclaration{Name: name, Description: t.Function.Description,
			ParametersJsonSchema: t.Function.Parameters})
		funcs[name] = func(_ context.Context, args map[string]any) map[string]any {
			o, _ := m.client.Call(name, args)
			return map[string]any{"status": o.Status, "text": o.Text}
		}
	}
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		names = append(names, d.Name)
	}
	log("metis_tools", map[string]any{"addr": m.ToolAddr, "tools": names})
	if len(decls) == 0 {
		return nil, funcs
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}, funcs
}

// Door listens for metis-home (and any other producer) and routes its lines to out.
func (m *Metis) Door(ctx context.Context, out chan<- Line, log func(string, map[string]any)) error {
	ln, err := net.Listen("tcp", m.DoorAddr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	log("door", map[string]any{"addr": ln.Addr().String()})
	for {
		c, err := ln.Accept()
		if err != nil {
			return nil
		}
		go func() {
			defer c.Close()
			sc := bufio.NewScanner(c)
			sc.Buffer(make([]byte, 1<<16), 1<<20)
			for sc.Scan() {
				if l, ok := m.route(sc.Bytes()); ok {
					out <- l
				}
			}
		}()
	}
}

func (m *Metis) route(b []byte) (Line, bool) {
	var ev struct {
		Stream string  `json:"stream"`
		Key    string  `json:"key"`
		Text   *string `json:"text"`
		Level  string  `json:"level"`
	}
	if json.Unmarshal(b, &ev) != nil || ev.Text == nil || strings.TrimSpace(*ev.Text) == "" {
		return Line{}, false
	}
	l := Line{Text: *ev.Text, Kind: "other"}
	switch ev.Stream {
	case "env":
		l.Kind = "env"
		if ev.Key != "" {
			l.Text = ev.Key + ": " + l.Text
		}
	case "reasoning":
		// metis-home's lines: "<package> <status>: …", "<package> noticed: …", "<package> surprise: …"
		head, _, _ := strings.Cut(l.Text, ":")
		switch f := strings.Fields(head); {
		case len(f) >= 2 && f[1] == "noticed":
			l.Kind = "noticed"
		case len(f) >= 2 && f[1] == "surprise":
			l.Kind = "surprise"
		case len(f) >= 2 && f[1] == "error":
			l.Kind = "error"
		case len(f) >= 2:
			l.Kind = "result"
		}
	}
	l.Level = ev.Level
	if l.Level == "" {
		l.Level = m.Levels[l.Kind]
	}
	if l.Level == "" {
		l.Level = "context"
	}
	return l, l.Level != "drop"
}
