// Package ws is a minimal RFC 6455 WebSocket server implementation —
// exactly the subset tcm's localhost sidebar needs: HTTP upgrade, masked
// client frames (text, with fragmentation), unmasked server text frames,
// ping/pong, and close. No extensions are negotiated (permessage-deflate
// offers are declined by omission), no client-side handshake, no binary
// frames.
//
// Deliberately stdlib-only per house style; if QA ever shows protocol
// trouble with a real client, swapping this package for a maintained
// library is contained behind Upgrade/ReadText/WriteText.
package ws

import (
	"bufio"
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
	"sync/atomic"
)

// magicGUID is the fixed RFC 6455 accept-key suffix.
const magicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// maxMessageSize bounds a reassembled client message. Client commands are
// small JSON; 1 MiB is generous headroom.
const maxMessageSize = 1 << 20

// Frame opcodes (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// ErrClosed is returned by ReadText after a close frame or EOF.
var ErrClosed = errors.New("ws: connection closed")

// Conn is one upgraded WebSocket connection. ReadText must be called from
// a single goroutine; WriteText is safe for concurrent use.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex
	closed  atomic.Bool
}

// Upgrade performs the server-side RFC 6455 handshake and returns the
// connection. On failure it writes the HTTP error itself and returns nil
// with the error.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !headerContainsToken(r.Header, "Connection", "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "not a websocket handshake", http.StatusBadRequest)
		return nil, errors.New("ws: not an upgrade request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("ws: missing key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return nil, errors.New("ws: hijack unsupported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("ws: hijack: %w", err)
	}

	sum := sha1.Sum([]byte(key + magicGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws: handshake write: %w", err)
	}
	return &Conn{conn: conn, br: rw.Reader}, nil
}

// ReadText returns the next complete text message, transparently answering
// pings and swallowing pongs. It returns ErrClosed on a clean close frame
// and the underlying error otherwise.
func (c *Conn) ReadText() (string, error) {
	var message []byte
	inFragment := false
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return "", err
		}
		switch opcode {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return "", err
			}
		case opPong:
			// keepalive response — ignore
		case opClose:
			_ = c.writeFrame(opClose, nil)
			c.Close()
			return "", ErrClosed
		case opText, opBinary:
			if inFragment {
				return "", errors.New("ws: new data frame inside fragmented message")
			}
			message = append(message, payload...)
			if fin {
				return string(message), nil
			}
			inFragment = true
		case opContinuation:
			if !inFragment {
				return "", errors.New("ws: continuation without initial frame")
			}
			message = append(message, payload...)
			if len(message) > maxMessageSize {
				return "", errors.New("ws: message too large")
			}
			if fin {
				return string(message), nil
			}
		default:
			return "", fmt.Errorf("ws: unsupported opcode %#x", opcode)
		}
	}
}

// WriteText sends one unmasked text frame (server→client frames are never
// masked, RFC 6455 §5.1).
func (c *Conn) WriteText(s string) error {
	return c.writeFrame(opText, []byte(s))
}

// Close closes the underlying connection. Safe to call more than once.
func (c *Conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.conn.Close()
}

// readFrame reads one frame, unmasking the payload (client frames must be
// masked; unmasked client frames are a protocol error we tolerate by
// passing through, since the only client is our own TUI on localhost).
func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var header [2]byte
	if _, err = io.ReadFull(c.br, header[:]); err != nil {
		if c.closed.Load() || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, 0, nil, ErrClosed
		}
		return false, 0, nil, err
	}
	fin = header[0]&0x80 != 0
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > maxMessageSize {
		return false, 0, nil, errors.New("ws: frame too large")
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// writeFrame sends one complete (FIN) unmasked frame.
func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return ErrClosed
	}
	header := make([]byte, 0, 10)
	header = append(header, 0x80|opcode)
	switch n := len(payload); {
	case n < 126:
		header = append(header, byte(n))
	case n <= 0xFFFF:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

// headerContainsToken reports whether a comma-separated header contains a
// token, case-insensitively ("Connection: keep-alive, Upgrade").
func headerContainsToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
