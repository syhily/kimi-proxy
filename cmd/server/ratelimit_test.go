package main

import (
	"errors"
	"net"
	"testing"
	"time"

	"kimi-proxy/internal/tunnel"
)

func TestIPLimiterBurstAndRate(t *testing.T) {
	l := newIPLimiter(1, 3) // 1 token/s, burst 3

	// Burst capacity is available immediately.
	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("connection %d within burst should be allowed", i)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("connection beyond burst should be rejected")
	}
	// Other IPs are not affected.
	if !l.allow("5.6.7.8") {
		t.Fatal("unrelated IP must not be limited")
	}
}

func TestIPLimiterRefills(t *testing.T) {
	l := newIPLimiter(10, 1)
	if !l.allow("1.2.3.4") {
		t.Fatal("first connection should be allowed")
	}
	if l.allow("1.2.3.4") {
		t.Fatal("second connection should be rejected")
	}
	// 150ms at 10/s refills 1.5 tokens, capped by burst=1.
	time.Sleep(150 * time.Millisecond)
	if !l.allow("1.2.3.4") {
		t.Fatal("connection after refill should be allowed")
	}
	if l.allow("1.2.3.4") {
		t.Fatal("immediately after, the bucket is empty again")
	}
}

func TestIPBanLifetimeAndExpiry(t *testing.T) {
	b := newIPBan()
	b.ban("1.2.3.4", 100*time.Millisecond)
	if !b.blocked("1.2.3.4") {
		t.Fatal("banned IP must be blocked")
	}
	if b.blocked("5.6.7.8") {
		t.Fatal("unrelated IP must not be blocked")
	}
	time.Sleep(150 * time.Millisecond)
	if b.blocked("1.2.3.4") {
		t.Fatal("ban must expire")
	}
}

func TestIPBanZeroDurationDisabled(t *testing.T) {
	b := newIPBan()
	b.ban("1.2.3.4", 0)
	if b.blocked("1.2.3.4") {
		t.Fatal("zero-duration ban must not block anything")
	}
}

func TestIsBadCertificateAlert(t *testing.T) {
	if !isBadCertificateAlert(&net.OpError{Op: "remote error", Err: errors.New("tls: bad certificate")}) {
		t.Fatal("bad_certificate alert must be recognized")
	}
	if isBadCertificateAlert(&net.OpError{Op: "remote error", Err: errors.New("tls: timeout")}) {
		t.Fatal("unrelated remote alerts must not be treated as a certificate rejection")
	}
	if isBadCertificateAlert(errors.New("tls: handshake failure")) {
		t.Fatal("non-OpError failures must not be treated as a certificate rejection")
	}
	if isBadCertificateAlert(tunnel.ErrPeerIdentityMismatch) {
		t.Fatal("local identity mismatch is handled by errors.Is, not the alert check")
	}
}
