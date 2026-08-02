// Package tunnel provides the shared encrypted-tunnel plumbing used by both
// the kimi-proxy server and client: KCP (UDP) or TLS (TCP) transport, with
// smux multiplexing on top.
package tunnel

import (
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"net"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// DefaultPipeIdle bounds how long a proxied connection may stay silent before
// it is torn down. Active streams (including WebSockets) are unaffected
// because every read or write refreshes the deadline. It is a coarse backstop
// against slow connections; WebSocket upgrades are exempted from it entirely
// (see cmd/server).
const DefaultPipeIdle = 10 * time.Minute

// derive expands a pre-shared token into 32 bytes of key material for one
// specific purpose via HKDF-SHA256. Each purpose gets its own independent
// key, so the KCP cipher key and the TLS identity can never be confused with
// each other or with keys from other protocols using the same token.
func derive(token, purpose string) []byte {
	key, err := hkdf.Key(sha256.New, []byte("kimi-proxy/v2:"+token), nil, purpose, 32)
	if err != nil {
		// hkdf.Key only errors on invalid hash output length; 32 is always
		// valid for SHA-256, so this cannot happen.
		panic(err)
	}
	return key
}

// DeriveKCPKey turns a pre-shared token into the AES-256 key used by the KCP
// block cipher. Both sides must use the same token to talk at all.
func DeriveKCPKey(token string) []byte {
	return derive(token, "kimi-proxy/kcp/v2")
}

// DeriveTLSCertKey turns a pre-shared token into the 32-byte Ed25519 seed
// used to build the TLS identity for the TCP transport. It is derived from
// the same token as DeriveKCPKey but with a separate purpose, keeping the two
// keys independent.
func DeriveTLSCertKey(token string) []byte {
	return derive(token, "kimi-proxy/tls/v2")
}

// ListenKCP creates an AES-256-GCM encrypted KCP listener.
func ListenKCP(addr string, key []byte) (*kcp.Listener, error) {
	block, err := kcp.NewAESGCMCrypt(key)
	if err != nil {
		return nil, err
	}
	// No FEC (0, 0): KCP's ARQ is enough for this use case.
	return kcp.ListenWithOptions(addr, block, 0, 0)
}

// DialKCP creates an AES-256-GCM encrypted KCP connection to addr.
func DialKCP(addr string, key []byte) (*kcp.UDPSession, error) {
	block, err := kcp.NewAESGCMCrypt(key)
	if err != nil {
		return nil, err
	}
	conn, err := kcp.DialWithOptions(addr, block, 0, 0)
	if err != nil {
		return nil, err
	}
	Tune(conn)
	return conn, nil
}

// Tune applies stream-oriented KCP parameters (same idea as kcptun).
func Tune(conn *kcp.UDPSession) {
	conn.SetStreamMode(true)
	conn.SetNoDelay(1, 10, 2, 1)
	conn.SetWindowSize(256, 256)
	conn.SetMtu(1350)
}

// TuneConn applies transport tuning to a freshly accepted tunnel
// connection: KCP gets stream-oriented parameters, TCP/TLS gets low-latency
// and keepalive settings. Other transports are left untouched.
func TuneConn(conn net.Conn) {
	if uc, ok := conn.(*kcp.UDPSession); ok {
		Tune(uc)
		return
	}
	tuneTCP(conn)
}

// TCP keepalive parameters for tunnel connections. The idle is deliberately
// short (60s) and the probes aggressive (3 x 15s), so a half-open connection
// — a peer that vanished or a network black hole — is noticed at the TCP
// layer after ~105s, before the smux keepalive timeout would fire.
const (
	tcpKeepaliveIdle     = 60 * time.Second
	tcpKeepaliveInterval = 15 * time.Second
	tcpKeepaliveCount    = 3
)

// tuneTCP applies low-latency (TCP_NODELAY) and keepalive settings to a raw
// or TLS-wrapped TCP connection. Unsupported platforms silently skip the
// parts they cannot apply.
func tuneTCP(conn net.Conn) {
	var tc *net.TCPConn
	switch c := conn.(type) {
	case *net.TCPConn:
		tc = c
	case *tls.Conn:
		if nc, ok := c.NetConn().(*net.TCPConn); ok {
			tc = nc
		}
	default:
		return
	}
	_ = tc.SetNoDelay(true)
	_ = tc.SetKeepAliveConfig(net.KeepAliveConfig{
		Enable:   true,
		Idle:     tcpKeepaliveIdle,
		Interval: tcpKeepaliveInterval,
		Count:    tcpKeepaliveCount,
	})
}

// SmuxConfig returns the multiplexing configuration shared by both ends.
// The keepalive is bidirectional: each side sends a keepalive frame every
// KeepAliveInterval and tears the session down only after KeepAliveTimeout
// without any frame from the peer. The timeout is deliberately generous (90s
// vs the usual 30s) so that a burst of packet loss or a slow network does not
// kill an otherwise healthy session.
func SmuxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.KeepAliveTimeout = 90 * time.Second
	cfg.MaxFrameSize = 32768
	return cfg
}

// Pipe copies data bidirectionally between a and b, and closes both as soon
// as either direction finishes. idle bounds how long either side may stay
// silent: a direction that produces no traffic for idle is treated as stuck
// and tears the whole connection down, which keeps slow connections from
// holding streams and goroutines open forever. Every read or write refreshes
// the deadline, so busy streams are never affected. An idle <= 0 disables the
// timeout.
func Pipe(a, b net.Conn, idle time.Duration) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			if idle > 0 {
				_ = src.SetReadDeadline(time.Now().Add(idle))
			}
			n, err := src.Read(buf)
			if n > 0 {
				if idle > 0 {
					_ = dst.SetWriteDeadline(time.Now().Add(idle))
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
}
