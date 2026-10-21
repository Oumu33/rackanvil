package web

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---- minimal in-process SSH server (real protocol, test-only) ----

func startTestSSHServer(t *testing.T, user, password string) (addr string, stop func()) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(pass) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("bad credentials")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go handleTestSSHConn(nc, cfg)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func handleTestSSHConn(nc net.Conn, cfg *ssh.ServerConfig) {
	defer nc.Close()
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				switch req.Type {
				case "pty-req", "shell":
					req.Reply(true, nil)
					if req.Type == "shell" {
						// Interactive echo: what the bridge test talks to.
						_, _ = io.Copy(channel, channel)
						return
					}
				case "exec":
					var payload struct{ Command string }
					_ = ssh.Unmarshal(req.Payload, &payload)
					req.Reply(true, nil)
					fmt.Fprintf(channel, "executed: %s\n", payload.Command)
					channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
					return
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}

// memFrameConn is an in-memory frameConn for bridge tests.
type memFrameConn struct {
	toBridge   chan []byte
	fromBridge chan []byte
	closed     chan struct{}
}

func newMemPair() (client, server *memFrameConn) {
	a2b := make(chan []byte, 32)
	b2a := make(chan []byte, 32)
	done := make(chan struct{})
	return &memFrameConn{toBridge: b2a, fromBridge: a2b, closed: done},
		&memFrameConn{toBridge: a2b, fromBridge: b2a, closed: done}
}

func (m *memFrameConn) ReadMessage() (int, []byte, error) {
	select {
	case p := <-m.toBridge:
		return wsText, p, nil
	case <-m.closed:
		return 0, nil, io.EOF
	}
}
func (m *memFrameConn) WriteMessage(op int, p []byte) error {
	cp := append([]byte(nil), p...)
	select {
	case m.fromBridge <- cp:
		return nil
	case <-m.closed:
		return io.EOF
	}
}
func (m *memFrameConn) Close() error {
	select {
	case <-m.closed:
	default:
		close(m.closed)
	}
	return nil
}

func TestBridgeSSHRealProtocol(t *testing.T) {
	addr, stop := startTestSSHServer(t, "test", "s3cret")
	defer stop()

	client, server := newMemPair()
	done := make(chan error, 1)
	go func() { done <- bridgeSSH(server, addr, "test", "s3cret") }()

	// Speak to the shell through the bridge: the test server echoes.
	if err := client.WriteMessage(wsText, []byte("ping-bridge-123\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	var got []byte
loop:
	for {
		select {
		case p := <-client.fromBridge:
			got = append(got, p...)
			if strings.Contains(string(got), "ping-bridge-123") {
				break loop
			}
		case <-deadline:
			t.Fatalf("timeout waiting for echo, got %q", got)
		case err := <-done:
			t.Fatalf("bridge exited early: %v (got %q)", err, got)
		}
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not exit after client close")
	}
}

func TestBridgeSSHBadPassword(t *testing.T) {
	addr, stop := startTestSSHServer(t, "test", "s3cret")
	defer stop()
	_, server := newMemPair()
	if err := bridgeSSH(server, addr, "test", "wrong"); err == nil {
		t.Fatal("want auth error")
	} else if !strings.Contains(err.Error(), "ssh dial") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWSFramingRealHandshake exercises the real HTTP upgrade + framing
// against an echo handler (no mocks of our own code).
func TestWSFramingRealHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fc, err := wsHandshake(w, r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		defer fc.Close()
		for {
			op, p, err := fc.ReadMessage()
			if err != nil {
				return
			}
			if err := fc.WriteMessage(op, p); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("want 101, got %q err %v", status, err)
	}
	// skip headers
	for {
		line, _ := br.ReadString('\n')
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	// send masked "hello" text frame
	mask := []byte{1, 2, 3, 4}
	payload := []byte("hello")
	var frame []byte
	frame = append(frame, 0x81, 0x80|byte(len(payload)))
	frame = append(frame, mask...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	frame = append(frame, masked...)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	// read unmasked echo
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		t.Fatal(err)
	}
	if hdr[0] != 0x81 || hdr[1] != 5 {
		t.Fatalf("bad echo header %v", hdr)
	}
	echo := make([]byte, 5)
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != "hello" {
		t.Fatalf("want hello, got %q", echo)
	}
}
