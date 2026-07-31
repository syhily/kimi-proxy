// Package proto defines the control-channel messages exchanged between
// the kimi-proxy client and server over the first smux stream.
package proto

import (
	"encoding/json"
	"io"
)

// Version is the control protocol version.
const Version = 1

// Control message types.
const (
	TypeAuth     = "auth"      // client -> server, carries Token
	TypeAuthOK   = "auth_ok"   // server -> client
	TypeAuthFail = "auth_fail" // server -> client, carries Message
	TypePing     = "ping"      // client -> server heartbeat
)

// Message is a single JSON object (one line) on the control stream.
type Message struct {
	Type    string `json:"type"`
	Token   string `json:"token,omitempty"`
	Version int    `json:"version,omitempty"`
	Text    string `json:"text,omitempty"`
}

// Write encodes v as one JSON line.
func Write(w io.Writer, v *Message) error {
	return json.NewEncoder(w).Encode(v)
}

// Read decodes one JSON object, bounded to 4 KiB.
func Read(r io.Reader, v *Message) error {
	return json.NewDecoder(io.LimitReader(r, 4096)).Decode(v)
}
