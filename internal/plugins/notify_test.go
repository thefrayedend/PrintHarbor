package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The bundled notify plugin must turn a print_finished event into a webhook POST
// with the project and printer in it — the reference event-hook plugin working
// end to end, and the path the companion app's push and Discord both use.
func TestNotifyPlugin(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
		w.WriteHeader(204) // like Discord — no body, so the plugin must not choke
	}))
	defer srv.Close()

	data := t.TempDir()
	m := New(data)
	m.Start(context.Background())
	defer m.Stop()
	if why := m.Off(); why != "" {
		t.Skipf("plugin host unavailable: %s", why)
	}
	// notify ships off; turn it on and point it at the test webhook.
	m.SetOff("notify", false)
	m.SetSettings("notify", map[string]any{"webhook_url": srv.URL, "on_finished": true})

	m.dispatch(map[string]any{
		"type": "print_finished", "printer_name": "Creator 5", "project_name": "Dragon", "plate": 1,
	})

	select {
	case body := <-got:
		var msg map[string]any
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			t.Fatalf("webhook body isn't JSON: %q", body)
		}
		content, _ := msg["content"].(string)
		if !strings.Contains(content, "Dragon") || !strings.Contains(content, "Creator 5") || !strings.Contains(content, "finished") {
			t.Errorf("webhook content missing details: %q", content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notify plugin never called the webhook")
	}

	// With no channel configured, a disabled-by-settings event must send nothing.
	m.SetSettings("notify", map[string]any{"webhook_url": srv.URL, "on_finished": false})
	m.dispatch(map[string]any{"type": "print_finished", "printer_name": "X", "project_name": "Y"})
	select {
	case body := <-got:
		t.Errorf("sent a notification though on_finished is off: %q", body)
	case <-time.After(500 * time.Millisecond):
		// good — nothing sent
	}
}
