// kimi-proxy-client runs on your own machine. It spawns `kimi web`, keeps an
// encrypted KCP tunnel to the public server, and forwards incoming streams to
// the local kimi web port.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kimi-proxy/internal/kimiweb"
	"kimi-proxy/internal/proto"
	"kimi-proxy/internal/tunnel"

	"github.com/xtaci/smux"
)

const authTimeout = 15 * time.Second

func main() {
	server := flag.String("server", "", "public server tunnel address, host:port (required)")
	token := flag.String("token", "", "pre-shared token (or KIMI_PROXY_TOKEN env)")
	kimiBin := flag.String("kimi-bin", "kimi", "path to the kimi CLI")
	kimiPort := flag.Int("kimi-port", 0, "port for kimi web; 0 picks a free port")
	publicHost := flag.String("public-host", "", "public domain used to reach the server; passed to kimi web --allowed-host and used in the access URL hint")
	attach := flag.String("attach", "", "attach to an already-running kimi web (host:port) instead of spawning one")
	flag.Parse()

	if *server == "" {
		log.Fatal("-server is required")
	}
	if *token == "" {
		*token = os.Getenv("KIMI_PROXY_TOKEN")
	}
	if *token == "" {
		log.Fatal("token is required: use -token or KIMI_PROXY_TOKEN")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	local := *attach
	if local == "" {
		port := *kimiPort
		if port == 0 {
			var err error
			port, err = kimiweb.FreePort()
			if err != nil {
				log.Fatalf("pick free port: %v", err)
			}
		}
		args := []string{"web", "--port", fmt.Sprint(port), "--no-open"}
		if *publicHost != "" {
			args = append(args, "--allowed-host", *publicHost)
		}
		sup := &kimiweb.Supervisor{Bin: *kimiBin, Args: args}
		go sup.Run(ctx)
		local = fmt.Sprintf("127.0.0.1:%d", port)
	} else {
		log.Printf("attaching to existing kimi web at %s", local)
	}

	log.Printf("waiting for kimi web at %s ...", local)
	if err := kimiweb.WaitReady(local, 60*time.Second); err != nil {
		log.Fatalf("kimi web not ready: %v", err)
	}
	log.Printf("kimi web is ready at %s", local)
	printAccessHint(*server, *publicHost)

	key := tunnel.DeriveKey(*token)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := runTunnel(ctx, *server, key, *token, local)
		if ctx.Err() != nil {
			return
		}
		log.Printf("tunnel down: %v; reconnecting in %v", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// runTunnel establishes one authenticated tunnel session and serves streams
// until the connection breaks.
func runTunnel(ctx context.Context, server string, key []byte, token, local string) error {
	conn, err := tunnel.DialKCP(server, key)
	if err != nil {
		return fmt.Errorf("dial %s: %w", server, err)
	}
	sess, err := smux.Client(conn, tunnel.SmuxConfig())
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smux client: %w", err)
	}
	defer sess.Close()

	// Control stream: authenticate, then heartbeat.
	ctrl, err := sess.OpenStream()
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
	if err := proto.Write(ctrl, &proto.Message{Type: proto.TypeAuth, Version: proto.Version, Token: token}); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}
	var reply proto.Message
	if err := proto.Read(ctrl, &reply); err != nil {
		return fmt.Errorf("read auth reply: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if reply.Type != proto.TypeAuthOK {
		return fmt.Errorf("auth rejected: %s", reply.Text)
	}
	log.Printf("tunnel established to %s", server)

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = sess.Close()
				return
			case <-ticker.C:
				if err := proto.Write(ctrl, &proto.Message{Type: proto.TypePing}); err != nil {
					_ = sess.Close()
					return
				}
			}
		}
	}()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}
		go func() {
			upstream, err := net.DialTimeout("tcp", local, 10*time.Second)
			if err != nil {
				log.Printf("dial local kimi web %s: %v", local, err)
				_ = stream.Close()
				return
			}
			tunnel.Pipe(upstream, stream)
		}()
	}
}

// printAccessHint prints the URL to open in the browser, including the
// persistent bearer token if it can be read from the kimi home directory.
func printAccessHint(server, publicHost string) {
	host := publicHost
	if host == "" {
		host = strings.Split(server, ":")[0]
	}
	token, err := os.ReadFile(serverTokenPath())
	if err != nil {
		log.Printf("access the web UI at http://%s (log in with your kimi web bearer token)", host)
		return
	}
	log.Printf("access the web UI at http://%s/#token=%s", host, strings.TrimSpace(string(token)))
}

func serverTokenPath() string {
	home := os.Getenv("KIMI_CODE_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".kimi-code")
		}
	}
	return filepath.Join(home, "server.token")
}
