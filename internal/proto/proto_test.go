package proto

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msg := &Message{Type: TypeAuth, Version: Version, Token: "sekret"}
	if err := Write(&buf, msg); err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := Read(&buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != msg.Type || got.Version != msg.Version || got.Token != msg.Token {
		t.Fatalf("roundtrip mismatch: %+v != %+v", got, msg)
	}
}

func TestReadRejectsOversizedInput(t *testing.T) {
	big := `{"type":"auth","token":"` + strings.Repeat("x", 8192) + `"}`
	if err := Read(strings.NewReader(big), &Message{}); err == nil {
		t.Fatal("expected error for a message beyond the 4 KiB budget")
	}
}
