package printers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A server that behaves like real FlashForge firmware for the upload: it needs
// a real Content-Length (not chunked), the exact header spellings, and it does
// the Expect: 100-continue handshake. Proves the raw-socket Upload works where
// net/http's chunked streaming got a 500. It also answers /detail (the status
// call Upload makes first when start=true), so the test is a realistic send.
func TestFlashForgeRawUploadLikeFirmware(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	up := struct {
		seen                           bool
		chunked, had100, exact, isPOST bool
		printNow                       string
		bodyLen                        int
		filename                       bool
	}{}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				reqLine, _ := br.ReadString('\n')
				headers := map[string]string{}
				var raw []string
				for {
					h, err := br.ReadString('\n')
					if h == "\r\n" || h == "\n" || h == "" || err != nil {
						break
					}
					raw = append(raw, strings.TrimRight(h, "\r\n"))
					if i := strings.Index(h, ":"); i > 0 {
						headers[strings.TrimSpace(h[:i])] = strings.TrimSpace(h[i+1:])
					}
				}
				switch {
				case strings.HasPrefix(reqLine, "POST /detail"):
					// read the JSON body, answer a plain printing status
					if n, _ := strconv.Atoi(headers["Content-Length"]); n > 0 {
						io.ReadFull(br, make([]byte, n))
					}
					body := `{"code":0,"message":"Success","detail":{"status":"printing","matlStationInfo":{"slotInfos":[{"slotId":1,"materialName":"PLA","materialColor":"#FF0000","hasFilament":true}]}}}`
					fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
				case strings.HasPrefix(reqLine, "POST /uploadGcode"):
					mu.Lock()
					up.seen, up.isPOST = true, true
					up.exact = headers["serialNumber"] != "" && headers["printNow"] != "" && headers["fileSize"] != "" && headers["Content-Length"] != ""
					up.printNow = headers["printNow"]
					for _, h := range raw {
						if strings.EqualFold(h, "Transfer-Encoding: chunked") {
							up.chunked = true
						}
					}
					mu.Unlock()
					if up.chunked || headers["Content-Length"] == "" {
						fmt.Fprint(c, "HTTP/1.1 500 Internal Server Error\r\nContent-Length: 0\r\n\r\n")
						return
					}
					if strings.Contains(strings.ToLower(headers["Expect"]), "100-continue") {
						fmt.Fprint(c, "HTTP/1.1 100 Continue\r\n\r\n")
						mu.Lock()
						up.had100 = true
						mu.Unlock()
					}
					n, _ := strconv.Atoi(headers["Content-Length"])
					body := make([]byte, n)
					io.ReadFull(br, body)
					mu.Lock()
					up.bodyLen = n
					up.filename = strings.Contains(string(body), `filename="a.gcode"`) && strings.Contains(string(body), "G28")
					mu.Unlock()
					fmt.Fprint(c, "HTTP/1.1 200 OK\r\nContent-Length: 30\r\n\r\n{\"code\":0,\"message\":\"Success\"}")
				default:
					fmt.Fprint(c, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
				}
			}(c)
		}
	}()

	f, err := NewFlashForge("http://"+ln.Addr().String(), "SN1", "abc")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.Upload(WithSlotMap(ctx, []int{1}), "a.gcode", strings.NewReader("G28\nG1 X0"), true); err != nil {
		t.Fatalf("upload failed against firmware-like server: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !up.seen || !up.isPOST || !up.exact || up.chunked || !up.had100 || up.printNow != "true" || up.bodyLen == 0 || !up.filename {
		t.Fatalf("upload handshake wrong: %+v", up)
	}
}
