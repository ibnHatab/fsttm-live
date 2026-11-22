package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ibnHatab/fsttm-live/internal/live"
	"google.golang.org/genai"
)

// home is a stand-in for SOURCES until Metis is wired: two ventilation units with a state.
type home struct {
	mu    sync.Mutex
	units map[string]string
}

func newHome() *home {
	return &home{units: map[string]string{"lab ventilation": "off", "bedroom ventilation": "off"}}
}

func (h *home) tools() []*genai.Tool {
	str := func(d string) *genai.Schema { return &genai.Schema{Type: genai.TypeString, Description: d} }
	return []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
		Name:        "home_air",
		Description: "The home's ventilation units (Prana recuperators): one in the lab, one in the bedroom. Switch them, change their mode, or ask their state.",
		Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
			"device": str("the unit: 'lab ventilation' or 'bedroom ventilation'"),
			"intent": str("ON, OFF, NIGHT, BOOST or QUERY"),
		}, Required: []string{"device", "intent"}},
	}}}}
}

func (h *home) funcs() map[string]live.ToolFunc {
	return map[string]live.ToolFunc{"home_air": func(_ context.Context, args map[string]any) map[string]any {
		dev, _ := args["device"].(string)
		in, _ := args["intent"].(string)
		dev = strings.ToLower(strings.TrimSpace(dev))
		h.mu.Lock()
		defer h.mu.Unlock()
		st, ok := h.units[dev]
		if !ok {
			return map[string]any{"error": fmt.Sprintf("no unit %q; the units are 'lab ventilation' and 'bedroom ventilation'", dev)}
		}
		switch strings.ToUpper(in) {
		case "QUERY":
			return map[string]any{"device": dev, "state": st}
		case "ON":
			st = "on"
		case "OFF":
			st = "off"
		case "NIGHT":
			st = "on, night mode"
		case "BOOST":
			st = "on, boost"
		default:
			return map[string]any{"error": "intent must be ON, OFF, NIGHT, BOOST or QUERY"}
		}
		h.units[dev] = st
		return map[string]any{"device": dev, "state": st, "status": "done"}
	}}
}
