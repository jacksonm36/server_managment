package api

import (
	"bytes"
	"context"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

// The display console's VNC password stays in the panel. The panel answers
// Proxmox's VNC authentication itself, then tells the browser the display
// needs no password; from then on RFB passes through untouched. A browser
// (or anything reading its memory, history or network log) never holds a
// credential for the console port.

// wsStream reads a WebSocket's binary messages as one byte stream.
type wsStream struct {
	c   *websocket.Conn
	buf []byte
}

func (s *wsStream) read(ctx context.Context, n int) ([]byte, error) {
	for len(s.buf) < n {
		_, data, err := s.c.Read(ctx)
		if err != nil {
			return nil, err
		}
		s.buf = append(s.buf, data...)
	}
	out := s.buf[:n:n]
	s.buf = s.buf[n:]
	return out, nil
}

// rest returns what was read past the handshake, to pass on as is.
func (s *wsStream) rest() []byte {
	b := s.buf
	s.buf = nil
	return b
}

const (
	rfbVersion  = "RFB 003.008\n"
	rfbSecNone  = 1
	rfbSecVNC   = 2
	rfbMaxTypes = 32
)

// rfbReason reads a 4-byte length and that many bytes of failure text.
func rfbReason(ctx context.Context, s *wsStream) string {
	b, err := s.read(ctx, 4)
	if err != nil {
		return "no reason given"
	}
	n := binary.BigEndian.Uint32(b)
	if n > 1024 {
		return "no reason given"
	}
	txt, err := s.read(ctx, int(n))
	if err != nil {
		return "no reason given"
	}
	return string(txt)
}

// rfbLoginUpstream does the RFB 3.8 handshake with Proxmox up to and
// including the security result, using VNC authentication with password
// (or none, if that is all the server asks for).
func rfbLoginUpstream(ctx context.Context, up *wsStream, password string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := up.read(ctx, 12)
	if err != nil {
		return fmt.Errorf("display: %w", err)
	}
	if !bytes.HasPrefix(v, []byte("RFB 003.")) || string(v[8:11]) < "007" {
		return fmt.Errorf("display: unsupported RFB version %q", bytes.TrimSpace(v))
	}
	if err := up.c.Write(ctx, websocket.MessageBinary, []byte(rfbVersion)); err != nil {
		return fmt.Errorf("display: %w", err)
	}
	nb, err := up.read(ctx, 1)
	if err != nil {
		return fmt.Errorf("display: %w", err)
	}
	if nb[0] == 0 {
		return fmt.Errorf("display refused the connection: %s", rfbReason(ctx, up))
	}
	if nb[0] > rfbMaxTypes {
		return errors.New("display: bad security type list")
	}
	types, err := up.read(ctx, int(nb[0]))
	if err != nil {
		return fmt.Errorf("display: %w", err)
	}
	var pick byte
	switch {
	case bytes.IndexByte(types, rfbSecVNC) >= 0 && password != "":
		pick = rfbSecVNC
	case bytes.IndexByte(types, rfbSecNone) >= 0:
		pick = rfbSecNone
	default:
		return fmt.Errorf("display: no supported authentication (offered %v)", types)
	}
	if err := up.c.Write(ctx, websocket.MessageBinary, []byte{pick}); err != nil {
		return fmt.Errorf("display: %w", err)
	}
	if pick == rfbSecVNC {
		challenge, err := up.read(ctx, 16)
		if err != nil {
			return fmt.Errorf("display: %w", err)
		}
		resp, err := vncAuthResponse(password, challenge)
		if err != nil {
			return err
		}
		if err := up.c.Write(ctx, websocket.MessageBinary, resp); err != nil {
			return fmt.Errorf("display: %w", err)
		}
	}
	res, err := up.read(ctx, 4)
	if err != nil {
		return fmt.Errorf("display: %w", err)
	}
	if binary.BigEndian.Uint32(res) != 0 {
		return fmt.Errorf("display login refused: %s", rfbReason(ctx, up))
	}
	return nil
}

// rfbGreetBrowser plays the server side of the handshake for noVNC,
// offering only "no authentication": the panel already logged in.
func rfbGreetBrowser(ctx context.Context, br *wsStream) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := br.c.Write(ctx, websocket.MessageBinary, []byte(rfbVersion)); err != nil {
		return err
	}
	v, err := br.read(ctx, 12)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(v, []byte("RFB 003.00")) {
		return errors.New("not an RFB client")
	}
	if err := br.c.Write(ctx, websocket.MessageBinary, []byte{1, rfbSecNone}); err != nil {
		return err
	}
	pick, err := br.read(ctx, 1)
	if err != nil {
		return err
	}
	if pick[0] != rfbSecNone {
		return errors.New("client picked an unoffered security type")
	}
	return br.c.Write(ctx, websocket.MessageBinary, []byte{0, 0, 0, 0})
}

// vncAuthResponse encrypts the server's challenge with DES, keyed by the
// first 8 bytes of the password with each byte's bits reversed (the VNC
// quirk).
func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	copy(key, password)
	for i, b := range key {
		var r byte
		for j := 0; j < 8; j++ {
			r = r<<1 | (b>>j)&1
		}
		key[i] = r
	}
	c, err := des.NewCipher(key) // #nosec G502 -- RFB's VNC auth is DES by definition
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	c.Encrypt(out[:8], challenge[:8])
	c.Encrypt(out[8:], challenge[8:16])
	return out, nil
}
