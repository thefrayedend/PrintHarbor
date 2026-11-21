package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/library"
	"github.com/thefrayedend/printharbor/internal/printers"
)

// The assistant answers questions about the workshop with a local model
// served by Ollama. It can read printers, projects, the queue and the
// library through tools. Anything that changes a printer (pause, resume,
// stop, light) is only proposed; it happens when you press Confirm.

type chatMsg struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls []ollamaToolUse `json:"tool_calls,omitempty"`
}

type ollamaToolUse struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// PendingAction is a printer action the assistant proposed.
type PendingAction struct {
	ID          string `json:"id"`
	PrinterID   string `json:"printer_id"`
	PrinterName string `json:"printer_name"`
	Action      string `json:"action"`
	Label       string `json:"label"`
}

type assistantState struct {
	mu      sync.Mutex
	pending map[string]PendingAction
}

var assistantTools = []map[string]any{
	tool("get_printers", "List every printer with its state, progress, time left, temperatures and loaded filament.", nil),
	tool("get_projects", "List projects with how many plates are done, printing, waiting for review, or left.", nil),
	tool("get_queue", "List recent slicing jobs and their state.", nil),
	tool("search_library", "Search the local model library.", map[string]any{"query": map[string]any{"type": "string", "description": "words to search for"}}),
	tool("propose_printer_action", "Propose pausing, resuming, stopping, or switching the light of a printer. The user must confirm before it happens.",
		map[string]any{"printer": map[string]any{"type": "string", "description": "printer name"},
			"action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "cancel", "light_on", "light_off"}}}),
}

func tool(name, desc string, props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	req := []string{}
	for k := range props {
		req = append(req, k)
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": desc,
		"parameters": map[string]any{"type": "object", "properties": props, "required": req}}}
}

func (a *App) runTool(name string, args json.RawMessage, pending *[]PendingAction) string {
	var in map[string]string
	_ = json.Unmarshal(args, &in)
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	switch name {
	case "get_printers":
		var out []map[string]any
		for _, v := range a.Printers.List() {
			m := map[string]any{"name": v.Name, "state": string(v.Status.State), "ready_for_next_job": v.Ready}
			if v.Status.State == printers.StatePrinting || v.Status.State == printers.StatePaused {
				m["job"], m["progress_percent"], m["minutes_left"] = v.Status.JobName, int(v.Status.Progress*100), v.Status.RemainingSec/60
			}
			if v.Status.Nozzle > 0 {
				m["nozzle_c"], m["bed_c"] = int(v.Status.Nozzle), int(v.Status.Bed)
			}
			var fil []string
			for _, s := range v.LoadedSlots() {
				fil = append(fil, fmt.Sprintf("slot %d: %s %s", s.Index, s.Type, s.Color))
			}
			m["filament"] = fil
			out = append(out, m)
		}
		return j(out)
	case "get_projects":
		var out []map[string]any
		for _, p := range a.Projects() {
			need, done, printing, review := 0, 0, 0, 0
			for _, pp := range a.Progress(p) {
				need += pp.Needed
				done += pp.Accepted
				printing += pp.Printing
				review += pp.Review
			}
			out = append(out, map[string]any{"name": p.Name, "plates": len(p.Data.Plates), "prints_needed": need, "done": done,
				"printing": printing, "waiting_for_review": review, "left": max(0, need-done-printing-review)})
		}
		return j(out)
	case "get_queue":
		jobs := a.Queue.List()
		if len(jobs) > 15 {
			jobs = jobs[len(jobs)-15:]
		}
		var out []map[string]any
		for _, jb := range jobs {
			out = append(out, map[string]any{"project": jb.ProjectName, "plate": jb.Plate, "printer": jb.PrinterName, "state": string(jb.State), "error": jb.Error})
		}
		return j(out)
	case "search_library":
		items, total := a.Library.Search(library.Query{Text: in["query"], Limit: 10})
		var out []map[string]any
		for _, it := range items {
			out = append(out, map[string]any{"title": it.Title, "category": it.Category, "tags": it.Tags, "files": len(it.Files), "printed": it.PrintCount})
		}
		return j(map[string]any{"total": total, "items": out})
	case "propose_printer_action":
		for _, v := range a.Printers.List() {
			if strings.EqualFold(v.Name, strings.TrimSpace(in["printer"])) {
				labels := map[string]string{"pause": "Pause", "resume": "Resume", "cancel": "Stop the print on", "light_on": "Turn on the light of", "light_off": "Turn off the light of"}
				l, ok := labels[in["action"]]
				if !ok {
					return "unknown action"
				}
				pa := PendingAction{ID: runID(), PrinterID: v.ID, PrinterName: v.Name, Action: in["action"], Label: l + " " + v.Name}
				a.assistant.mu.Lock()
				a.assistant.pending[pa.ID] = pa
				a.assistant.mu.Unlock()
				*pending = append(*pending, pa)
				return "Proposed. The user will see a Confirm button; it has NOT happened yet."
			}
		}
		return "no printer with that name"
	}
	return "unknown tool"
}

// Ask runs one assistant turn (with tool use) and returns the reply.
func (a *App) Ask(ctx context.Context, history []chatMsg) (string, []PendingAction, error) {
	url := strings.TrimRight(a.cfg().OllamaURL, "/")
	if url == "" {
		return "", nil, fmt.Errorf("the assistant isn't set up: add an Ollama address in Settings")
	}
	model := a.cfg().OllamaModel
	if model == "" {
		model = "qwen2.5:7b"
	}
	sys := chatMsg{Role: "system", Content: "You are PrintHarbor's workshop assistant. You help the user run their 3D printers. " +
		"Use the tools to look things up instead of guessing. Keep answers short and practical. Never claim a printer action happened: " +
		"actions are only proposed and the user confirms them. Today is " + time.Now().Format("Monday 2 January 2006, 15:04") + "."}
	msgs := append([]chatMsg{sys}, history...)
	var pending []PendingAction
	for step := 0; step < 6; step++ {
		body, _ := json.Marshal(map[string]any{"model": model, "messages": msgs, "tools": assistantTools, "stream": false})
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		req, _ := http.NewRequestWithContext(cctx, "POST", url+"/api/chat", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return "", nil, fmt.Errorf("can't reach Ollama: %w", err)
		}
		var out struct {
			Message chatMsg `json:"message"`
			Error   string  `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		cancel()
		if err != nil {
			return "", nil, err
		}
		if out.Error != "" {
			return "", nil, fmt.Errorf("ollama: %s", out.Error)
		}
		if len(out.Message.ToolCalls) == 0 {
			return strings.TrimSpace(out.Message.Content), pending, nil
		}
		msgs = append(msgs, out.Message)
		for _, tc := range out.Message.ToolCalls {
			msgs = append(msgs, chatMsg{Role: "tool", Content: a.runTool(tc.Function.Name, tc.Function.Arguments, &pending)})
		}
	}
	return "I looked a few things up but couldn't finish the answer. Try asking more simply.", pending, nil
}

func (a *App) assistantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/assistant", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []chatMsg `json:"messages"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if len(in.Messages) > 30 {
			in.Messages = in.Messages[len(in.Messages)-30:]
		}
		// Only user/assistant text comes from the browser.
		var clean []chatMsg
		for _, m := range in.Messages {
			if m.Role == "user" || m.Role == "assistant" {
				clean = append(clean, chatMsg{Role: m.Role, Content: m.Content})
			}
		}
		reply, pending, err := a.Ask(r.Context(), clean)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if pending == nil {
			pending = []PendingAction{}
		}
		writeJSON(w, 200, map[string]any{"reply": reply, "pending": pending})
	})
	mux.HandleFunc("POST /api/assistant/confirm/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.assistant.mu.Lock()
		pa, ok := a.assistant.pending[r.PathValue("id")]
		delete(a.assistant.pending, r.PathValue("id"))
		a.assistant.mu.Unlock()
		if !ok {
			fail(w, 404, fmt.Errorf("that request expired; ask again"))
			return
		}
		if err := a.Printers.Action(r.Context(), pa.PrinterID, pa.Action); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]string{"done": pa.Label})
	})
}
