package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestHandleHTTPNoClient verifies that with no tunnel client registered the
// public HTTP entry answers a 503 that must not be cached.
func TestHandleHTTPNoClient(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()

	go handleHTTP(serverSide, time.Minute)

	if err := clientSide.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// With no client registered the server answers 503 without reading the
	// request, so there is nothing to send; just read the response.
	resp, err := http.ReadResponse(bufio.NewReader(clientSide), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected Cache-Control: no-store, got %q", got)
	}
}

func TestIsWebSocketUpgrade(t *testing.T) {
	cases := []struct {
		name string
		req  string
		want bool
	}{
		{
			name: "websocket upgrade",
			req:  "GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\n\r\n",
			want: true,
		},
		{
			name: "websocket upgrade lowercase header",
			req:  "GET /ws HTTP/1.1\r\nhost: x\r\nupgrade: WebSocket\r\n\r\n",
			want: true,
		},
		{
			name: "plain get",
			req:  "GET / HTTP/1.1\r\nHost: x\r\n\r\n",
			want: false,
		},
		{
			name: "post cannot upgrade",
			req:  "POST /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n\r\n",
			want: false,
		},
		{
			name: "empty request",
			req:  "",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := newProbeConn(&fakeConn{Reader: strings.NewReader(tc.req)})
			if got := isWebSocketUpgrade(pc); got != tc.want {
				t.Fatalf("isWebSocketUpgrade() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWebSocketProbeDoesNotConsumeData verifies that the probe spools what it
// reads and the wrapped connection still delivers every byte, exactly once.
func TestWebSocketProbeDoesNotConsumeData(t *testing.T) {
	req := "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n\r\n"
	pc := newProbeConn(&fakeConn{Reader: strings.NewReader(req)})
	if !isWebSocketUpgrade(pc) {
		t.Fatal("expected websocket upgrade")
	}
	got, err := io.ReadAll(pc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != req {
		t.Fatalf("probe lost or duplicated data: got %q, want %q", got, req)
	}
}

// TestPlainRequestSurvivesProbe verifies the same for a non-upgrade request
// (the common case), where the probe reads through the whole header block.
func TestPlainRequestSurvivesProbe(t *testing.T) {
	req := "GET / HTTP/1.1\r\nHost: x\r\n\r\nbody-bytes-follow"
	pc := newProbeConn(&fakeConn{Reader: strings.NewReader(req)})
	if isWebSocketUpgrade(pc) {
		t.Fatal("expected plain request")
	}
	got, err := io.ReadAll(pc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != req {
		t.Fatalf("probe lost or duplicated data: got %q, want %q", got, req)
	}
}

// fakeConn is a minimal net.Conn for probe tests: reading from a string,
// writes and deadlines discarded.
type fakeConn struct {
	net.Conn // nil; only Read/SetReadDeadline are used
	*strings.Reader
	deadline time.Time
}

func (f *fakeConn) Read(b []byte) (int, error) { return f.Reader.Read(b) }
func (f *fakeConn) SetReadDeadline(t time.Time) error {
	f.deadline = t
	return nil
}
