package plugins

import (
	"context"
	"log"
	"sync"
	"time"
)

// Event hooks let any plugin react to things happening in PrintHarbor — a print
// starting, finishing or failing, a bed being cleared, a printer changing state.
// A plugin opts in by defining an on_event(ctx, event=None) function and, if it
// wants only some events, listing them in "events" in its plugin.json. The event
// is a dict with at least "type" and "time"; the rest depends on the type (see
// docs/plugins/events.md). This is the foundation the notification plugin and
// other automations are built on, so one plugin can watch the whole workshop.

// recentEvents is a small ring of the last events emitted, for the UI and tests
// to confirm events are flowing. It is independent of whether any plugin is on.
type recentRing struct {
	mu   sync.Mutex
	buf  []map[string]any
	size int
}

var recent = &recentRing{size: 50}

func (r *recentRing) add(ev map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, ev)
	if len(r.buf) > r.size {
		r.buf = r.buf[len(r.buf)-r.size:]
	}
}

// RecentEvents returns the most recent events, newest last.
func (m *Manager) RecentEvents() []map[string]any {
	recent.mu.Lock()
	defer recent.mu.Unlock()
	out := make([]map[string]any, len(recent.buf))
	copy(out, recent.buf)
	return out
}

// wantsEvent reports whether a loaded plugin should receive an event of this
// type: it must define on_event, be on and healthy, and either list the type in
// "events" (or "*"), or list no events at all (then it gets everything).
func wantsEvent(man Manifest, evType string) bool {
	if man.Off || man.Error != "" {
		return false
	}
	has := false
	for _, fn := range man.Functions {
		if fn == "on_event" {
			has = true
			break
		}
	}
	if !has {
		return false
	}
	if len(man.Events) == 0 {
		return true
	}
	for _, e := range man.Events {
		if e == "*" || e == evType {
			return true
		}
	}
	return false
}

// eventQueue is how many events may wait for the plugins. Events are small and
// handlers quick; this only fills if a plugin hangs for a long time.
const eventQueue = 256

// eventLimit is how long one plugin may take over one event.
var eventLimit = 20 * time.Second

// Emit sends an event to every interested plugin, without blocking the caller:
// a slow or broken handler must never hold up a print. Events are delivered in
// the order they happened, one at a time, by a single background worker. (They
// used to get a goroutine each, so "print started" could arrive after the
// state change that followed it, and a stuck plugin left goroutines piling up.)
// If the queue is ever full the event is dropped and logged rather than
// blocking. Emit is safe to call on a nil Manager (plugins unavailable).
func (m *Manager) Emit(ev map[string]any) {
	if m == nil || ev == nil {
		return
	}
	if ev["time"] == nil {
		ev["time"] = time.Now().Format(time.RFC3339)
	}
	recent.add(ev)
	m.evOnce.Do(func() {
		m.evQueue = make(chan map[string]any, eventQueue)
		go func() {
			for ev := range m.evQueue {
				m.dispatch(ev)
			}
		}()
	})
	select {
	case m.evQueue <- ev:
	default:
		log.Printf("plugins: too many events waiting (a plugin is stuck?); dropped %v", ev["type"])
	}
}

// dispatch delivers an event to matching plugins, one after the other. The
// worker started by Emit runs it; tests call it directly.
func (m *Manager) dispatch(ev map[string]any) {
	evType, _ := ev["type"].(string)
	for _, man := range m.List() {
		if !wantsEvent(man, evType) {
			continue
		}
		if err := m.rpcWithin(context.Background(), eventLimit, man.ID, "on_event", map[string]any{"event": ev}, nil); err != nil {
			log.Printf("plugin %s on_event(%s): %v", man.ID, evType, err)
		}
	}
}
