package printers

// Protocol capture: a passive second MQTT listener that logs everything the
// printer reports AND the commands Bambu's own software (Bambu Studio / Handy)
// sends it, on the local broker, using the LAN access code. Nothing is sent to
// the printer. This is how a new model's protocol — the H3C tool changer, say
// — gets learned: run a tool change or a multi-colour print from Bambu's app,
// and the exact JSON lands in the log for PrintHarbor to match.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capture is a running protocol log. Close it to stop.
type Capture struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	client  *mqttClient
	count   int
	lastAt  time.Time
	started time.Time
	done    bool
}

// captureLine is one logged MQTT message.
type captureLine struct {
	T     string          `json:"t"`   // RFC3339 timestamp
	Dir   string          `json:"dir"` // "report" (printer→) or "command" (app→printer)
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data,omitempty"` // the JSON payload
	Raw   string          `json:"raw,omitempty"`  // payload as text, if it isn't JSON
}

// StartCapture opens a log file and begins recording. Only Bambu printers
// carry the local MQTT protocol, so only they can be captured.
func StartCapture(p Printer, path string) (*Capture, error) {
	if p.Driver != "bambu" {
		return nil, fmt.Errorf("protocol capture is for Bambu Lab printers")
	}
	host, serial, code := strings.TrimSpace(p.Address), strings.TrimSpace(p.Serial), strings.TrimSpace(p.APIKey)
	if host == "" || serial == "" || code == "" {
		return nil, fmt.Errorf("the printer needs its IP address, serial number and LAN access code first")
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	cap := &Capture{f: f, path: path, started: time.Now()}
	tlsCfg := &tls.Config{InsecureSkipVerify: true, ServerName: serial}
	// Subscribe to both the report topic (printer → apps) and the request
	// topic (apps → printer); the local broker delivers both to any client
	// holding the access code.
	subs := []string{"device/" + serial + "/report", "device/" + serial + "/request"}
	c, _, err := mqttDial(net.JoinHostPort(host, strconv.Itoa(8883)), "printharbor-capture", "bblp", code, tlsCfg, subs,
		func(topic string, payload []byte) { cap.write(topic, payload) })
	if err != nil {
		f.Close()
		os.Remove(path)
		if strings.Contains(err.Error(), "access code") {
			return nil, fmt.Errorf("the printer refused the LAN access code")
		}
		return nil, fmt.Errorf("couldn't connect to the printer: %v", err)
	}
	cap.client = c
	return cap, nil
}

func (c *Capture) write(topic string, payload []byte) {
	dir := "command"
	if strings.HasSuffix(topic, "/report") {
		dir = "report"
	}
	line := captureLine{T: time.Now().Format(time.RFC3339Nano), Dir: dir, Topic: topic}
	if json.Valid(payload) {
		line.Data = json.RawMessage(payload)
	} else {
		line.Raw = string(payload)
	}
	b, _ := json.Marshal(line)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.f.Write(append(b, '\n'))
	c.count++
	c.lastAt = time.Now()
}

// Stats reports progress for the UI.
func (c *Capture) Stats() (count int, started, last time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, c.started, c.lastAt
}

func (c *Capture) Path() string { return c.path }

// Close stops the capture and closes the file.
func (c *Capture) Close() {
	c.mu.Lock()
	if c.done {
		c.mu.Unlock()
		return
	}
	c.done = true
	cl := c.client
	c.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
	c.mu.Lock()
	c.f.Close()
	c.mu.Unlock()
}
