package printers

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// mjpegFrame grabs one JPEG from an MJPEG (multipart/x-mixed-replace)
// stream, or returns the body directly if the URL already serves a JPEG.
func mjpegFrame(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("camera: %s", resp.Status)
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "image/") {
		return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	}
	mt, params, _ := mime.ParseMediaType(ct)
	if strings.HasPrefix(mt, "multipart/") && params["boundary"] != "" {
		mr := multipart.NewReader(resp.Body, strings.TrimPrefix(params["boundary"], "--"))
		part, err := mr.NextPart()
		if err != nil {
			return nil, err
		}
		return io.ReadAll(io.LimitReader(part, 10<<20))
	}
	// Unknown framing: scan for a JPEG between SOI and EOI markers.
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, br, 4<<20); err != nil && err != io.EOF {
		if buf.Len() == 0 {
			return nil, err
		}
	}
	b := buf.Bytes()
	s := bytes.Index(b, []byte{0xFF, 0xD8})
	if s >= 0 {
		if e := bytes.Index(b[s:], []byte{0xFF, 0xD9}); e > 0 {
			return b[s : s+e+2], nil
		}
	}
	return nil, fmt.Errorf("no picture found in the camera stream")
}
