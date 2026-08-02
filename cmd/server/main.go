// kimi-proxy-server runs on the public server. It accepts an encrypted KCP
// tunnel from the client and proxies incoming HTTP connections through it.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"kimi-proxy/internal/proto"
	"kimi-proxy/internal/tunnel"

	"github.com/xtaci/smux"
)

const (
	authTimeout   = 15 * time.Second
	headerTimeout = 15 * time.Second
	shutdownGrace = 5 * time.Second
)

var current atomic.Value // holds *smux.Session of the connected client

func main() {
	tunnelAddr := flag.String("tunnel-addr", ":7000", "tunnel listen address (KCP/UDP, or TCP with -tunnel-proto tcp)")
	httpAddr := flag.String("http-addr", ":8080", "public HTTP listen address (TCP)")
	token := flag.String("token", "", "pre-shared token (or KIMI_PROXY_TOKEN env)")
	tunnelProto := flag.String("tunnel-proto", "kcp", "tunnel transport: kcp (UDP) or tcp (TLS)")
	httpMaxConns := flag.Int("http-max-conns", 256, "maximum concurrent public HTTP connections")
	httpIdleTimeout := flag.Duration("http-idle-timeout", tunnel.DefaultPipeIdle, "close proxied connections idle for this long (0 disables)")
	tunnelMaxConns := flag.Int("tunnel-max-conns", 8, "maximum concurrent tunnel connections, including unauthenticated ones")
	tlsCert := flag.String("tls-cert", "", "TLS certificate for the public HTTP entry (requires -tls-key); enables HTTPS directly")
	tlsKey := flag.String("tls-key", "", "TLS private key for the public HTTP entry")
	flag.Parse()

	if *token == "" {
		*token = os.Getenv("KIMI_PROXY_TOKEN")
	}
	if *token == "" {
		log.Fatal("token is required: use -token or KIMI_PROXY_TOKEN")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("-tls-cert and -tls-key must be provided together")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSlots := make(chan struct{}, *httpMaxConns)
	tunnelSlots := make(chan struct{}, *tunnelMaxConns)

	var ln net.Listener
	switch *tunnelProto {
	case "kcp":
		kl, err := tunnel.ListenKCP(*tunnelAddr, tunnel.DeriveKCPKey(*token))
		if err != nil {
			log.Fatalf("listen tunnel %s: %v", *tunnelAddr, err)
		}
		ln = kl
	case "tcp":
		tl, err := tunnel.ListenTCP(*tunnelAddr, *token)
		if err != nil {
			log.Fatalf("listen tunnel %s: %v", *tunnelAddr, err)
		}
		ln = tl
	default:
		log.Fatalf("unknown -tunnel-proto %q: want kcp or tcp", *tunnelProto)
	}
	log.Printf("tunnel listening on %s (%s)", *tunnelAddr, *tunnelProto)

	httpLn, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		log.Fatalf("listen http %s: %v", *httpAddr, err)
	}
	if *tlsCert != "" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("load tls key pair: %v", err)
		}
		httpLn = tls.NewListener(httpLn, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		log.Printf("http listening on %s (TLS)", *httpAddr)
	} else {
		log.Printf("http listening on %s", *httpAddr)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		serveTunnel(ctx, ln, tunnelSlots, *token)
	}()
	go func() {
		defer wg.Done()
		serveHTTP(ctx, httpLn, httpSlots, *httpIdleTimeout)
	}()

	<-ctx.Done()
	log.Printf("shutting down")
	_ = ln.Close()
	_ = httpLn.Close()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		log.Printf("timed out waiting for in-flight connections, exiting")
	}
}

// serveTunnel accepts tunnel connections until the listener is closed or the
// context is cancelled. A slot semaphore bounds the number of concurrent
// connections, including unauthenticated ones, so a flood of connections
// cannot exhaust goroutines and smux sessions.
func serveTunnel(ctx context.Context, ln net.Listener, slots chan struct{}, token string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept tunnel: %v", err)
			return
		}
		tunnel.TuneConn(conn)
		select {
		case slots <- struct{}{}:
		default:
			log.Printf("tunnel connection limit reached, rejecting %s", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			handleTunnelConn(conn, token)
		}()
	}
}

// serveHTTP accepts public HTTP connections until the listener is closed or
// the context is cancelled, bounding concurrency with a slot semaphore so
// that a connection flood cannot open unlimited streams to the client.
func serveHTTP(ctx context.Context, ln net.Listener, slots chan struct{}, idle time.Duration) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept http: %v", err)
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			write503(conn, "kimi-proxy: too many concurrent connections")
			continue
		}
		go func() {
			defer func() { <-slots }()
			handleHTTP(conn, idle)
		}()
	}
}

// handleTunnelConn authenticates a new client connection and, on success,
// registers its smux session as the active one.
func handleTunnelConn(conn net.Conn, token string) {
	sess, err := smux.Server(conn, tunnel.SmuxConfig())
	if err != nil {
		log.Printf("smux server: %v", err)
		_ = conn.Close()
		return
	}

	// The first stream must be the control stream carrying the auth message.
	_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
	ctrl, err := sess.AcceptStream()
	if err != nil {
		log.Printf("accept control stream: %v", err)
		_ = sess.Close()
		return
	}
	var msg proto.Message
	if err := proto.Read(ctrl, &msg); err != nil {
		log.Printf("read auth message: %v", err)
		_ = sess.Close()
		return
	}
	ok := msg.Type == proto.TypeAuth &&
		msg.Version == proto.Version &&
		len(msg.Token) <= proto.MaxTokenLen &&
		subtle.ConstantTimeCompare([]byte(msg.Token), []byte(token)) == 1
	if !ok {
		_ = proto.Write(ctrl, &proto.Message{Type: proto.TypeAuthFail, Text: "invalid token or protocol version"})
		log.Printf("rejected client %s: bad auth", conn.RemoteAddr())
		_ = sess.Close()
		return
	}
	if err := proto.Write(ctrl, &proto.Message{Type: proto.TypeAuthOK}); err != nil {
		_ = sess.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	// Latest client wins: replace any previous session.
	if old, ok := current.Swap(sess).(*smux.Session); ok && old != nil {
		log.Printf("client %s replaced the previous session", conn.RemoteAddr())
		_ = old.Close()
	}
	log.Printf("client registered: %s", conn.RemoteAddr())

	// Read heartbeats until the control stream breaks, then unregister.
	// Every ping is answered with a pong so the client can tell the control
	// path is alive end to end.
	for {
		if err := proto.Read(ctrl, &msg); err != nil {
			break
		}
		if msg.Type == proto.TypePing {
			if err := proto.Write(ctrl, &proto.Message{Type: proto.TypePong}); err != nil {
				break
			}
		}
	}
	// atomic.Value cannot hold nil; use a typed nil pointer as the "no
	// session" sentinel so CompareAndSwap does not panic.
	if current.CompareAndSwap(sess, (*smux.Session)(nil)) {
		log.Printf("client unregistered: %s", conn.RemoteAddr())
	}
	_ = sess.Close()
}

// handleHTTP proxies one public HTTP connection to the client through a new
// smux stream. When no client is connected it answers a plain 503.
func handleHTTP(conn net.Conn, idle time.Duration) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(60 * time.Second)
	}
	sess, _ := current.Load().(*smux.Session)
	if sess == nil || sess.IsClosed() {
		write503(conn, "kimi-proxy: no client connected")
		return
	}
	stream, err := sess.OpenStream()
	if err != nil {
		write503(conn, "kimi-proxy: failed to open tunnel stream")
		return
	}
	// WebSocket connections legitimately stay silent for minutes (while the
	// user reads a reply or the model thinks), so they must not be killed by
	// the idle timeout. The probe spools everything it reads, so the stream
	// itself is unaffected.
	pc := newProbeConn(conn)
	if isWebSocketUpgrade(pc) {
		idle = 0
	}
	tunnel.Pipe(pc, stream, idle)
}

// probeConn lets the WebSocket probe consume the request headers without
// losing them. While record is set, every byte read from the wire is copied
// into spool; the outward Read replays the spool first, then continues from
// the buffered reader, so the connection handed to the pipe is byte-identical
// to the original.
type probeConn struct {
	net.Conn
	r      *bufio.Reader
	spool  bytes.Buffer
	record bool
}

func newProbeConn(conn net.Conn) *probeConn {
	p := &probeConn{Conn: conn, record: true}
	p.r = bufio.NewReader(&probeSource{p})
	return p
}

// probeSource is the read path bufio uses; it feeds the spool while the
// probe is active.
type probeSource struct{ p *probeConn }

func (s *probeSource) Read(b []byte) (int, error) {
	n, err := s.p.Conn.Read(b)
	if n > 0 && s.p.record {
		_, _ = s.p.spool.Write(b[:n])
	}
	return n, err
}

// Read is the outward path used by the tunnel pipe: it replays what the
// probe consumed, then continues from the buffer.
func (p *probeConn) Read(b []byte) (int, error) {
	if p.spool.Len() > 0 {
		return p.spool.Read(b)
	}
	return p.r.Read(b)
}

// finish ends the probe: it stops spooling and trims the spool down to the
// bytes the probe actually consumed. Bytes that were read off the wire but
// are still sitting in the bufio buffer are not duplicated — they flow out
// through the buffered reader after the spool is exhausted.
func (p *probeConn) finish() {
	p.record = false
	consumed := p.spool.Len() - p.r.Buffered()
	if consumed < 0 {
		consumed = 0
	}
	p.spool.Truncate(consumed)
}

// isWebSocketUpgrade reports whether the first request on the connection
// asks for a WebSocket upgrade (a GET with an "Upgrade: websocket" header).
// Reads up to the end of the request headers, bounded to 64 KiB and 64 lines,
// with a short deadline so a peer that sends nothing while we wait cannot
// hold the slot. On any doubt it returns false and lets the plain pipe handle
// the connection.
func isWebSocketUpgrade(pc *probeConn) bool {
	_ = pc.SetReadDeadline(time.Now().Add(headerTimeout))
	defer func() {
		_ = pc.SetReadDeadline(time.Time{})
		pc.finish()
	}()
	first := true
	total := 0
	for i := 0; i < 64; i++ {
		line, err := pc.r.ReadString('\n')
		if err != nil {
			return false
		}
		total += len(line)
		if total > 64*1024 {
			return false
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if first {
			// A WebSocket upgrade is always a GET request.
			if !strings.HasPrefix(trimmed, "GET ") {
				return false
			}
			first = false
			continue
		}
		if trimmed == "" {
			return false // end of headers, no upgrade seen
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "upgrade:") && strings.Contains(lower, "websocket") {
			return true
		}
	}
	return false
}

func write503(conn net.Conn, msg string) {
	body := fmt.Sprintf("<html><body><h1>503 Service Unavailable</h1><p>%s</p></body></html>", msg)
	fmt.Fprintf(conn, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: text/html; charset=utf-8\r\nCache-Control: no-store\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	_ = conn.Close()
}
