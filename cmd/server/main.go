// kimi-proxy-server runs on the public server. It accepts an encrypted KCP
// tunnel from the client and proxies incoming HTTP connections through it.
package main

import (
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	"kimi-proxy/internal/proto"
	"kimi-proxy/internal/tunnel"

	"github.com/xtaci/smux"
)

const authTimeout = 15 * time.Second

var current atomic.Value // holds *smux.Session of the connected client

func main() {
	tunnelAddr := flag.String("tunnel-addr", ":7000", "tunnel listen address (KCP/UDP, or TCP with -tunnel-proto tcp)")
	httpAddr := flag.String("http-addr", ":8080", "public HTTP listen address (TCP)")
	token := flag.String("token", "", "pre-shared token (or KIMI_PROXY_TOKEN env)")
	tunnelProto := flag.String("tunnel-proto", "kcp", "tunnel transport: kcp (UDP) or tcp (TLS)")
	flag.Parse()

	if *token == "" {
		*token = os.Getenv("KIMI_PROXY_TOKEN")
	}
	if *token == "" {
		log.Fatal("token is required: use -token or KIMI_PROXY_TOKEN")
	}

	key := tunnel.DeriveKey(*token)
	var ln net.Listener
	switch *tunnelProto {
	case "kcp":
		kl, err := tunnel.ListenKCP(*tunnelAddr, key)
		if err != nil {
			log.Fatalf("listen tunnel %s: %v", *tunnelAddr, err)
		}
		ln = kl
	case "tcp":
		tl, err := tunnel.ListenTCP(*tunnelAddr, key)
		if err != nil {
			log.Fatalf("listen tunnel %s: %v", *tunnelAddr, err)
		}
		ln = tl
	default:
		log.Fatalf("unknown -tunnel-proto %q: want kcp or tcp", *tunnelProto)
	}
	log.Printf("tunnel listening on %s (%s)", *tunnelAddr, *tunnelProto)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("accept tunnel: %v", err)
				return
			}
			tunnel.TuneConn(conn)
			go handleTunnelConn(conn, *token)
		}
	}()

	httpLn, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		log.Fatalf("listen http %s: %v", *httpAddr, err)
	}
	log.Printf("http listening on %s", *httpAddr)
	for {
		conn, err := httpLn.Accept()
		if err != nil {
			log.Fatalf("accept http: %v", err)
		}
		go handleHTTP(conn)
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
	for {
		if err := proto.Read(ctrl, &msg); err != nil {
			break
		}
	}
	if current.CompareAndSwap(sess, nil) {
		log.Printf("client unregistered: %s", conn.RemoteAddr())
	}
	_ = sess.Close()
}

// handleHTTP proxies one public HTTP connection to the client through a new
// smux stream. When no client is connected it answers a plain 503.
func handleHTTP(conn net.Conn) {
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
	tunnel.Pipe(conn, stream)
}

func write503(conn net.Conn, msg string) {
	body := fmt.Sprintf("<html><body><h1>503 Service Unavailable</h1><p>%s</p></body></html>", msg)
	fmt.Fprintf(conn, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	_ = conn.Close()
}
