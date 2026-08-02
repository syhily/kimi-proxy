package tunnel

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestDeriveKeysDeterministicAndPurposeSeparated(t *testing.T) {
	kcpA := DeriveKCPKey("token-a")
	if !bytes.Equal(kcpA, DeriveKCPKey("token-a")) {
		t.Fatal("same token must derive the same KCP key")
	}
	if bytes.Equal(kcpA, DeriveKCPKey("token-b")) {
		t.Fatal("different tokens must derive different KCP keys")
	}
	if len(kcpA) != 32 {
		t.Fatalf("KCP key must be 32 bytes for AES-256, got %d", len(kcpA))
	}
	if bytes.Equal(kcpA, DeriveTLSCertKey("token-a")) {
		t.Fatal("KCP key and TLS identity must be derived independently")
	}
	if len(DeriveTLSCertKey("token-a")) != 32 {
		t.Fatal("TLS identity seed must be 32 bytes for Ed25519")
	}
}

// TestPipeBidirectionalAndClosePropagation verifies that data flows both ways
// and that Pipe returns once either side closes, closing both connections.
func TestPipeBidirectionalAndClosePropagation(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Pipe(a1, b1, time.Minute)
	}()

	want := []byte("client-to-server payload")
	go func() {
		_, _ = b2.Write(want)
		_ = b2.Close()
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(a2, got); err != nil {
		t.Fatalf("read through pipe: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload mismatch: %q != %q", got, want)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Pipe did not return after one side closed")
	}
	// Teardown must have closed both underlying connections.
	if _, err := a2.Read(make([]byte, 1)); err == nil {
		t.Fatal("a2 should be closed after pipe teardown")
	}
	if _, err := b2.Read(make([]byte, 1)); err == nil {
		t.Fatal("b2 should be closed after pipe teardown")
	}
}

// TestPipeIdleTimeoutTearsDown verifies that a connection that goes silent
// past the idle budget is torn down, closing both ends.
func TestPipeIdleTimeoutTearsDown(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Pipe(a1, b1, 100*time.Millisecond)
	}()

	if _, err := b2.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	// Now both sides go silent; the deadline must fire and close everything.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pipe did not tear down on idle")
	}
	if _, err := a2.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle timeout should have closed the connection")
	}
}

// TestPipeIdleDisabled verifies idle=0 keeps the connection open indefinitely.
func TestPipeIdleDisabled(t *testing.T) {
	a1, _ := net.Pipe()
	b1, b2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Pipe(a1, b1, 0)
	}()

	if _, err := b2.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("Pipe must stay open with idle disabled")
	case <-time.After(200 * time.Millisecond):
	}
}
