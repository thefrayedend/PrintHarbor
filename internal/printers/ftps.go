package printers

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ftpsSession is an implicit-FTPS connection (TLS from the first byte, as
// Bambu printers use on port 990). Data connections are TLS too and reuse
// the control session, which Bambu's server requires.
type ftpsSession struct {
	ctrl *tls.Conn
	br   *bufio.Reader
	cfg  *tls.Config
	d    *net.Dialer
	tmo  time.Duration
}

func ftpsLogin(addr, user, pass string, tlsCfg *tls.Config, timeout time.Duration) (*ftpsSession, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	cfg := tlsCfg.Clone()
	if cfg.ClientSessionCache == nil {
		cfg.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	}
	// TLS 1.2: session-ID resumption on the data channel works with
	// vsftpd's require_ssl_reuse, which TLS 1.3 tickets don't satisfy.
	cfg.MaxVersion = tls.VersionTLS12
	ctrl, err := tls.DialWithDialer(d, "tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	_ = ctrl.SetDeadline(time.Now().Add(timeout))
	s := &ftpsSession{ctrl: ctrl, br: bufio.NewReader(ctrl), cfg: cfg, d: d, tmo: timeout}
	if _, err := s.cmd("", 220); err != nil {
		ctrl.Close()
		return nil, err
	}
	if _, err := s.cmd("USER "+user, 331, 230); err != nil {
		ctrl.Close()
		return nil, err
	}
	if _, err := s.cmd("PASS "+pass, 230, 202); err != nil {
		ctrl.Close()
		if strings.Contains(err.Error(), "530") {
			return nil, fmt.Errorf("printer refused the access code")
		}
		return nil, err
	}
	for _, c := range []string{"PBSZ 0", "PROT P", "TYPE I"} {
		if _, err := s.cmd(c, 200); err != nil {
			ctrl.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *ftpsSession) Close() {
	_, _ = s.cmd("QUIT", 221)
	s.ctrl.Close()
}

func (s *ftpsSession) read() (int, string, error) {
	var msg strings.Builder
	for {
		line, err := s.br.ReadString('\n')
		if err != nil {
			return 0, "", err
		}
		msg.WriteString(line)
		// Multi-line replies end with "NNN " (space after the code).
		if len(line) >= 4 && line[3] == ' ' {
			code, _ := strconv.Atoi(line[:3])
			return code, strings.TrimSpace(msg.String()), nil
		}
	}
}

func (s *ftpsSession) cmd(c string, want ...int) (string, error) {
	if c != "" {
		if _, err := fmt.Fprintf(s.ctrl, "%s\r\n", c); err != nil {
			return "", err
		}
	}
	code, msg, err := s.read()
	if err != nil {
		return "", err
	}
	for _, w := range want {
		if code == w {
			return msg, nil
		}
	}
	shown := c
	if strings.HasPrefix(c, "PASS ") {
		shown = "PASS ****"
	}
	return msg, fmt.Errorf("ftp %q: %s", shown, msg)
}

// transfer opens a passive data connection for one command (STOR, LIST...).
func (s *ftpsSession) transfer(command string, fn func(conn *tls.Conn) error) error {
	msg, err := s.cmd("PASV", 227)
	if err != nil {
		return err
	}
	dataAddr, err := parsePASV(msg, s.ctrl.RemoteAddr())
	if err != nil {
		return err
	}
	raw, err := s.d.Dial("tcp", dataAddr)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.ctrl, "%s\r\n", command); err != nil {
		raw.Close()
		return err
	}
	if code, msg, err := s.read(); err != nil || (code != 150 && code != 125) {
		raw.Close()
		if err == nil {
			err = fmt.Errorf("ftp %s: %s", strings.Fields(command)[0], msg)
		}
		return err
	}
	data := tls.Client(raw, s.cfg)
	_ = data.SetDeadline(time.Now().Add(s.tmo))
	if err := data.Handshake(); err != nil {
		raw.Close()
		return fmt.Errorf("ftp data connection: %w", err)
	}
	ferr := fn(data)
	data.Close()
	if _, err := s.cmd("", 226, 250); err != nil && ferr == nil {
		ferr = err
	}
	return ferr
}

// Store uploads r as remote.
func (s *ftpsSession) Store(remote string, r io.Reader) error {
	return s.transfer("STOR "+remote, func(c *tls.Conn) error {
		_, err := io.Copy(c, r)
		return err
	})
}

// ftpEntry is one line of a directory listing.
type ftpEntry struct {
	Name  string
	Size  int64
	IsDir bool
}

// List lists a directory (Unix "ls -l" style LIST output, as vsftpd sends).
func (s *ftpsSession) List(dir string) ([]ftpEntry, error) {
	var out []ftpEntry
	err := s.transfer("LIST "+dir, func(c *tls.Conn) error {
		b, err := io.ReadAll(io.LimitReader(c, 4<<20))
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if e, ok := parseListLine(strings.TrimRight(line, "\r")); ok {
				out = append(out, e)
			}
		}
		return nil
	})
	return out, err
}

func parseListLine(line string) (ftpEntry, bool) {
	f := strings.Fields(line)
	if len(f) < 9 {
		return ftpEntry{}, false
	}
	// -rw-r--r-- 1 user group SIZE Mon DD HH:MM NAME (name may have spaces)
	idx := 0
	for i := 0; i < 8; i++ {
		j := strings.Index(line[idx:], f[i])
		idx += j + len(f[i])
	}
	name := strings.TrimSpace(line[idx:])
	if name == "." || name == ".." || name == "" {
		return ftpEntry{}, false
	}
	size, _ := strconv.ParseInt(f[4], 10, 64)
	return ftpEntry{Name: name, Size: size, IsDir: strings.HasPrefix(f[0], "d")}, true
}

// Delete removes a file.
func (s *ftpsSession) Delete(remote string) error {
	_, err := s.cmd("DELE "+remote, 250)
	return err
}

// ftpsUpload stores one file in a fresh session.
func ftpsUpload(addr, user, pass string, tlsCfg *tls.Config, remote string, r io.Reader, timeout time.Duration) error {
	s, err := ftpsLogin(addr, user, pass, tlsCfg, timeout)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Store(remote, r)
}

func parsePASV(msg string, ctrl net.Addr) (string, error) {
	i, j := strings.IndexByte(msg, '('), strings.IndexByte(msg, ')')
	if i < 0 || j < i {
		return "", fmt.Errorf("bad PASV reply %q", msg)
	}
	p := strings.Split(msg[i+1:j], ",")
	if len(p) != 6 {
		return "", fmt.Errorf("bad PASV reply %q", msg)
	}
	hi, _ := strconv.Atoi(strings.TrimSpace(p[4]))
	lo, _ := strconv.Atoi(strings.TrimSpace(p[5]))
	// Use the control connection's host: printers sometimes report 0.0.0.0.
	host, _, _ := net.SplitHostPort(ctrl.String())
	return net.JoinHostPort(host, strconv.Itoa(hi*256+lo)), nil
}
