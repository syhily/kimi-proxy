// Package tunnel provides the shared KCP + smux plumbing used by both the
// kimi-proxy server and client.
package tunnel

import (
	"crypto/sha256"
	"io"
	"net"
	"time"

	kcp "github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// DeriveKey turns a pre-shared token into the AES-256 key used by the KCP
// block cipher. Both sides must use the same token to talk at all.
func DeriveKey(token string) []byte {
	sum := sha256.Sum256([]byte("kimi-proxy/v1:" + token))
	return sum[:]
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

// SmuxConfig returns the multiplexing configuration shared by both ends.
func SmuxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.KeepAliveTimeout = 30 * time.Second
	cfg.MaxFrameSize = 32768
	return cfg
}

// Pipe copies data bidirectionally between a and b, and closes both as soon
// as either direction finishes.
func Pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
}
