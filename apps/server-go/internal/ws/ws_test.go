package ws

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// clientConn is a minimal RFC 6455 *client* used only by these tests:
// it masks outgoing frames (mandatory client→server) and reads unmasked
// server frames — the mirror of the implementation under test.
type clientConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialTestServer(t *testing.T, url string) *clientConn {
	t.Helper()
	addr := strings.TrimPrefix(url, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)
	req := "GET / HTTP/1.1\r\nHost: " + addr + "\r\n" +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("handshake status = %q err = %v", status, err)
	}
	for { // drain headers
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return &clientConn{conn: conn, br: br}
}

func (c *clientConn) writeFrame(opcode byte, payload []byte, fin bool) error {
	head := byte(opcode)
	if fin {
		head |= 0x80
	}
	frame := []byte{head}
	switch n := len(payload); {
	case n < 126:
		frame = append(frame, byte(n)|0x80) // masked
	case n <= 0xFFFF:
		frame = append(frame, 126|0x80, byte(n>>8), byte(n))
	default:
		frame = append(frame, 127|0x80)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		frame = append(frame, ext[:]...)
	}
	mask := []byte{0x12, 0x34, 0x56, 0x78}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, err := c.conn.Write(frame)
	return err
}

func (c *clientConn) readTextFrame(t *testing.T) string {
	t.Helper()
	var header [2]byte
	if _, err := readFull(c.br, header[:]); err != nil {
		t.Fatal(err)
	}
	if header[0]&0x0F != opText {
		t.Fatalf("opcode = %#x, want text", header[0]&0x0F)
	}
	length := int(header[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := readFull(c.br, ext[:]); err != nil {
			t.Fatal(err)
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := readFull(c.br, ext[:]); err != nil {
			t.Fatal(err)
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	payload := make([]byte, length)
	if _, err := readFull(c.br, payload); err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func readFull(br *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := br.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// echoServer upgrades and echoes every text message back.
func echoServer(t *testing.T, connCh chan<- *Conn) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			return
		}
		connCh <- conn
		go func() {
			for {
				msg, err := conn.ReadText()
				if err != nil {
					return
				}
				if err := conn.WriteText("echo:" + msg); err != nil {
					return
				}
			}
		}()
	}))
}

func TestHandshakeAndEcho(t *testing.T) {
	connCh := make(chan *Conn, 1)
	srv := echoServer(t, connCh)
	defer srv.Close()
	c := dialTestServer(t, srv.URL)
	defer c.conn.Close()

	if err := c.writeFrame(opText, []byte(`{"type":"refresh"}`), true); err != nil {
		t.Fatal(err)
	}
	if got := c.readTextFrame(t); got != `echo:{"type":"refresh"}` {
		t.Errorf("echo = %q", got)
	}
}

func TestFragmentedClientMessage(t *testing.T) {
	connCh := make(chan *Conn, 1)
	srv := echoServer(t, connCh)
	defer srv.Close()
	c := dialTestServer(t, srv.URL)
	defer c.conn.Close()

	if err := c.writeFrame(opText, []byte("hello "), false); err != nil {
		t.Fatal(err)
	}
	if err := c.writeFrame(opContinuation, []byte("world"), true); err != nil {
		t.Fatal(err)
	}
	if got := c.readTextFrame(t); got != "echo:hello world" {
		t.Errorf("fragmented echo = %q", got)
	}
}

func TestPingGetsPong(t *testing.T) {
	connCh := make(chan *Conn, 1)
	srv := echoServer(t, connCh)
	defer srv.Close()
	c := dialTestServer(t, srv.URL)
	defer c.conn.Close()

	if err := c.writeFrame(opPing, []byte("hb"), true); err != nil {
		t.Fatal(err)
	}
	var header [2]byte
	if _, err := readFull(c.br, header[:]); err != nil {
		t.Fatal(err)
	}
	if header[0]&0x0F != opPong {
		t.Fatalf("opcode = %#x, want pong", header[0]&0x0F)
	}
	payload := make([]byte, header[1]&0x7F)
	if _, err := readFull(c.br, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "hb" {
		t.Errorf("pong payload = %q", payload)
	}
}

func TestCloseFrame(t *testing.T) {
	// No echo loop here: the Conn contract is single-reader, so this test
	// owns ReadText itself and drives the close handshake directly.
	connCh := make(chan *Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, err := Upgrade(w, r); err == nil {
			connCh <- conn
		}
	}))
	defer srv.Close()
	c := dialTestServer(t, srv.URL)
	defer c.conn.Close()
	serverConn := <-connCh

	if err := c.writeFrame(opClose, nil, true); err != nil {
		t.Fatal(err)
	}
	// The read must consume the close frame, reply, and report ErrClosed —
	// and keep reporting it on subsequent calls.
	for i := 0; i < 2; i++ {
		if _, err := serverConn.ReadText(); !errors.Is(err, ErrClosed) {
			t.Fatalf("read %d after close = %v, want ErrClosed", i, err)
		}
	}
}

func TestUpgradeRejectsPlainRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Upgrade(w, r); err == nil {
			t.Error("plain GET must not upgrade")
		}
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
