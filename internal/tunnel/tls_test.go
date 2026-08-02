package tunnel

import (
	"testing"
	"time"
)

// TestTLSMutualAuth verifies the full handshake over a real TCP listener.
// A listener's Accept does not drive the TLS handshake — that only happens on
// the first read/write — so the test accepts connections in the background
// and reads from each one, which triggers (and verifies) the handshake:
//   - a client with a different token is rejected by the server;
//   - a client with the correct token completes a roundtrip.
// Each side derives its identity from its own token and pins the peer's
// certificate against it, so a token mismatch fails in both directions.
func TestTLSMutualAuth(t *testing.T) {
	ln, err := ListenTCP("127.0.0.1:0", "shared-token")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type acceptResult struct {
		data string
		err  error
	}
	accepted := make(chan acceptResult, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 8)
				n, err := c.Read(buf)
				if err != nil {
					accepted <- acceptResult{err: err}
					return
				}
				accepted <- acceptResult{data: string(buf[:n])}
			}()
		}
	}()

	addr := ln.Addr().String()

	// Wrong-token client: the client pins the server certificate (fails
	// locally) and the server pins the client certificate (fails there too);
	// either way the handshake must not complete.
	badConn, err := DialTCP(addr, "other-token")
	if err == nil {
		_ = badConn.Close()
		t.Fatal("client with the wrong token must not complete the handshake")
	}
	select {
	case r := <-accepted:
		if r.err == nil {
			t.Fatalf("server should have rejected the wrong-token client, got data %q", r.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not reject the wrong-token client")
	}

	// Correct-token client: handshake succeeds and data flows.
	conn, err := DialTCP(addr, "shared-token")
	if err != nil {
		t.Fatalf("handshake with the correct token failed: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-accepted:
		if r.err != nil {
			t.Fatalf("server read after correct handshake: %v", r.err)
		}
		if r.data != "ping" {
			t.Fatalf("payload mismatch: %q", r.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive the payload")
	}
}
