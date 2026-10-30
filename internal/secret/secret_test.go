package secret

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("api-key-123")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("api-key-123")) {
		t.Fatal("plaintext visible in sealed value")
	}
	if got, err := box.Open(sealed); err != nil || got != "api-key-123" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	other, _ := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("opening with another key must fail")
	}
	if _, err := New("short"); err == nil {
		t.Fatal("invalid key must be rejected")
	}
}
