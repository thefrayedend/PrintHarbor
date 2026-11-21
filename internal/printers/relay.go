package printers

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"sync"
	"time"
)

// camRelay keeps one connection to a printer's MJPEG camera and hands the
// frames to any number of viewers, like FlashBridge's MjpegRelay
// (android-tv-app/source/lib/core/services/camera_relay.dart). FlashForge
// cameras only accept one connection at a time, so every viewer, snapshot
// and AI check has to share it. The upstream closes 15 s after the last
// viewer leaves.
type camRelay struct {
	open func(ctx context.Context) (io.ReadCloser, error)

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	clients  map[chan []byte]struct{}
	latest   []byte
	latestAt time.Time
	lastErr  string
	idle     *time.Timer
	frameCh  chan struct{} // closed and replaced on every frame
}

func newCamRelay(open func(ctx context.Context) (io.ReadCloser, error)) *camRelay {
	return &camRelay{open: open, clients: map[chan []byte]struct{}{}, frameCh: make(chan struct{})}
}

func (r *camRelay) ensureRunning() {
	if r.idle != nil {
		r.idle.Stop()
		r.idle = nil
	}
	if r.running {
		return
	}
	r.running = true
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.loop(ctx)
}

func (r *camRelay) loop(ctx context.Context) {
	for ctx.Err() == nil {
		body, err := r.open(ctx)
		if err != nil {
			r.mu.Lock()
			r.lastErr = err.Error()
			r.mu.Unlock()
		} else {
			r.read(ctx, body)
			body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// read splits the stream into JPEGs by their start/end markers (works for
// any MJPEG framing), as FlashBridge does.
func (r *camRelay) read(ctx context.Context, body io.Reader) {
	br := bufio.NewReaderSize(body, 256<<10)
	buf := make([]byte, 64<<10)
	var data []byte
	for ctx.Err() == nil {
		n, err := br.Read(buf)
		data = append(data, buf[:n]...)
		for {
			soi := bytes.Index(data, []byte{0xFF, 0xD8})
			if soi < 0 {
				if len(data) > 0 && data[len(data)-1] == 0xFF {
					data = data[len(data)-1:]
				} else {
					data = data[:0]
				}
				break
			}
			eoi := bytes.Index(data[soi+2:], []byte{0xFF, 0xD9})
			if eoi < 0 {
				data = data[soi:]
				if len(data) > 8<<20 {
					data = data[:0]
				}
				break
			}
			end := soi + 2 + eoi + 2
			frame := append([]byte(nil), data[soi:end]...)
			data = data[end:]
			r.emit(frame)
		}
		if err != nil {
			r.mu.Lock()
			r.lastErr = "camera stream ended"
			r.mu.Unlock()
			return
		}
	}
}

func (r *camRelay) emit(frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latest, r.latestAt, r.lastErr = frame, time.Now(), ""
	close(r.frameCh)
	r.frameCh = make(chan struct{})
	for c := range r.clients {
		select {
		case c <- frame:
		default: // slow viewer: skip this frame
		}
	}
}

// subscribe returns a channel of frames and a function to leave.
func (r *camRelay) subscribe() (chan []byte, func()) {
	c := make(chan []byte, 2)
	r.mu.Lock()
	r.clients[c] = struct{}{}
	if r.latest != nil && time.Since(r.latestAt) < 3*time.Second {
		c <- r.latest
	}
	r.ensureRunning()
	r.mu.Unlock()
	return c, func() {
		r.mu.Lock()
		delete(r.clients, c)
		r.maybeStopLocked()
		r.mu.Unlock()
	}
}

func (r *camRelay) maybeStopLocked() {
	if len(r.clients) > 0 || !r.running {
		return
	}
	if r.idle != nil {
		r.idle.Stop()
	}
	r.idle = time.AfterFunc(15*time.Second, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.clients) == 0 && r.running {
			r.running = false
			r.cancel()
		}
	})
}

// snapshot returns a recent frame, starting the stream for a moment if
// nobody is watching.
func (r *camRelay) snapshot(ctx context.Context) ([]byte, string) {
	r.mu.Lock()
	if r.latest != nil && time.Since(r.latestAt) < 2*time.Second {
		f := r.latest
		r.mu.Unlock()
		return f, ""
	}
	r.ensureRunning()
	wait := r.frameCh
	r.maybeStopLocked()
	r.mu.Unlock()
	select {
	case <-wait:
	case <-ctx.Done():
	case <-time.After(8 * time.Second):
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latest != nil && time.Since(r.latestAt) < 10*time.Second {
		return r.latest, ""
	}
	if r.lastErr == "" {
		r.lastErr = "no picture from the camera"
	}
	return nil, r.lastErr
}
