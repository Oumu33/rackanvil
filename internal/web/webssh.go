// WebSSH: browser terminal over WebSocket bridged to an SSH target.
//
// Transport notes (honest):
//   - WebSocket framing is implemented with the standard library only
//     (http.Hijacker + RFC 6455 framing), no third-party WS dependency.
//   - SSH client is golang.org/x/crypto/ssh (standard, audited).
//   - Host key verification is InsecureIgnoreHostKey in this MVP; the
//     terminal page says so, and known_hosts pinning is documented as
//     follow-up work. Only reachable over networks you trust, like the
//     rest of this console (no TLS termination in the MVP either).
package web

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// frameConn abstracts a WebSocket message stream so the SSH bridge is
// unit-testable without a real HTTP handshake.
type frameConn interface {
	ReadMessage() (opcode int, p []byte, err error)
	WriteMessage(opcode int, p []byte) error
	Close() error
}

const (
	wsText   = 1
	wsBinary = 2
	wsClose  = 8
	wsPing   = 9
	wsPong   = 10
)

// ---- RFC 6455 framing over a hijacked conn ----

type rawWSConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex // serialize writes
}

func (c *rawWSConn) Close() error { return c.conn.Close() }

func (c *rawWSConn) ReadMessage() (int, []byte, error) {
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case wsText, wsBinary:
			return op, payload, nil
		case wsPing:
			_ = c.WriteMessage(wsPong, payload)
		case wsClose:
			return 0, nil, io.EOF
		default:
			// ignore continuation/control we don't need
		}
	}
}

func (c *rawWSConn) readFrame() (int, []byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c.br, hdr); err != nil {
		return 0, nil, err
	}
	opcode := int(hdr[0] & 0x0f)
	masked := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if length > 4<<20 {
		return 0, nil, errors.New("webssh: frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func (c *rawWSConn) WriteMessage(opcode int, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var hdr []byte
	hdr = append(hdr, 0x80|byte(opcode)) // FIN
	switch {
	case len(p) < 126:
		hdr = append(hdr, byte(len(p)))
	case len(p) < 65536:
		hdr = append(hdr, 126, byte(len(p)>>8), byte(len(p)))
	default:
		hdr = append(hdr, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(len(p)))
		hdr = append(hdr, ext[:]...)
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	_, err := c.conn.Write(p)
	return err
}

// wsHandshake validates the upgrade request and hijacks the connection.
func wsHandshake(w http.ResponseWriter, r *http.Request) (frameConn, error) {
	if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") ||
		strings.ToLower(r.Header.Get("Upgrade")) != "websocket" {
		return nil, errors.New("webssh: not a websocket handshake")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("webssh: missing Sec-WebSocket-Key")
	}
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("webssh: hijack not supported")
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	return &rawWSConn{conn: conn, br: bufrw.Reader}, nil
}

// ---- SSH bridge ----

// bridgeSSH connects fc (websocket side) to an SSH shell on target.
// Wire format: text frames carry terminal bytes both ways.
func bridgeSSH(fc frameConn, target, user, password string) error {
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // MVP: documented on the page
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", target, cfg)
	if err != nil {
		return fmt.Errorf("webssh: ssh dial: %w", err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("webssh: new session: %w", err)
	}
	defer sess.Close()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm", 40, 120, modes); err != nil {
		return fmt.Errorf("webssh: pty: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}
	if err := sess.Shell(); err != nil {
		return fmt.Errorf("webssh: shell: %w", err)
	}

	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	// ssh -> websocket
	go func() {
		defer closeDone()
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if werr := fc.WriteMessage(wsText, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer closeDone()
		_, _ = io.Copy(io.Discard, stderr)
	}()
	// websocket -> ssh
	go func() {
		defer closeDone()
		for {
			op, p, err := fc.ReadMessage()
			if err != nil {
				return
			}
			if op != wsText && op != wsBinary {
				continue
			}
			if _, err := stdin.Write(p); err != nil {
				return
			}
		}
	}()
	<-done
	return nil
}

// one-time terminal session tokens: token -> creds (60s TTL)
type termCreds struct {
	target, user, password string
	expiry                 time.Time
}

var termTokens = struct {
	sync.Mutex
	m map[string]termCreds
}{m: map[string]termCreds{}}

func mintTermToken(target, user, password string) string {
	var b [16]byte
	rand.Read(b[:])
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	termTokens.Lock()
	termTokens.m[tok] = termCreds{target: target, user: user, password: password, expiry: time.Now().Add(60 * time.Second)}
	// opportunistic cleanup
	for k, v := range termTokens.m {
		if time.Now().After(v.expiry) {
			delete(termTokens.m, k)
		}
	}
	termTokens.Unlock()
	return tok
}

func takeTermToken(tok string) (termCreds, bool) {
	termTokens.Lock()
	defer termTokens.Unlock()
	c, ok := termTokens.m[tok]
	if !ok || time.Now().After(c.expiry) {
		delete(termTokens.m, tok)
		return termCreds{}, false
	}
	delete(termTokens.m, tok)
	return c, true
}

// handleTerminal renders the connect form / terminal page.
func (w *Web) handleTerminal(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if r.Method == http.MethodPost {
		if !w.canOperate(u) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		target := strings.TrimSpace(r.FormValue("target"))
		user := strings.TrimSpace(r.FormValue("user"))
		password := r.FormValue("password")
		if target == "" || user == "" {
			w.render(rw, "terminal", map[string]any{"User": u, "Error": "target 和 user 必填"})
			return
		}
		if _, _, err := net.SplitHostPort(target); err != nil {
			target = net.JoinHostPort(target, "22")
		}
		tok := mintTermToken(target, user, password)
		_ = w.db.Audit(u.Username, "terminal.open", target+" as "+user)
		w.render(rw, "terminal_session", map[string]any{"User": u, "Token": tok, "Target": target, "User2": user})
		return
	}
	w.render(rw, "terminal", map[string]any{"User": u})
}

// handleWS upgrades to WebSocket and bridges to SSH.
func (w *Web) handleWS(rw http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	creds, ok := takeTermToken(tok)
	if !ok {
		http.Error(rw, "bad or expired token", http.StatusForbidden)
		return
	}
	fc, err := wsHandshake(rw, r)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	defer fc.Close()
	_ = fc.WriteMessage(wsText, []byte("\r\n[rackanvil] connecting to "+creds.target+" ...\r\n"))
	if err := bridgeSSH(fc, creds.target, creds.user, creds.password); err != nil {
		_ = fc.WriteMessage(wsText, []byte("\r\n[rackanvil] "+err.Error()+"\r\n"))
	}
}
