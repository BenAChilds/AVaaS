package auth

import "testing"

func TestURLCipher(t *testing.T) {
	c, err := NewURLCipher([]byte("a secret used only by this unit test"))
	if err != nil {
		t.Fatal(err)
	}
	raw := "https://bucket.example/file?X-Amz-Signature=secret"
	encrypted, err := c.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(encrypted) == raw {
		t.Fatal("URL stored in plaintext")
	}
	got, err := c.Decrypt(encrypted)
	if err != nil || got != raw {
		t.Fatalf("round trip: %q %v", got, err)
	}
}
