package app

import (
	"bytes"
	"testing"
)

func TestSecretCipherAuthenticatedRoundTrip(t *testing.T) {
	c, err := newCipher("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	a, err := seal(c, []byte("test-credential"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := seal(c, []byte("test-credential"))
	if bytes.Equal(a, b) || bytes.Contains(a, []byte("test-credential")) {
		t.Fatal("not randomized encryption")
	}
	plain, err := unseal(c, a)
	if err != nil || string(plain) != "test-credential" {
		t.Fatal("roundtrip", err)
	}
	a[len(a)-1] ^= 1
	if _, err = unseal(c, a); err == nil {
		t.Fatal("accepted tampered ciphertext")
	}
	if _, err = newCipher("short"); err == nil {
		t.Fatal("weak key accepted")
	}
}

func TestLiteralPersonIDValidation(t *testing.T) {
	for _, id := range []string{"123", "4567"} {
		if _, err := personID(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"0123", " 123", "123 ", "123abc", "-1", "0", "1/../2"} {
		if _, err := personID(id); err == nil {
			t.Errorf("accepted malformed token %q", id)
		}
	}
	id, err := personID("https://mdblist.com/actor/123/")
	if err != nil || id != "123" {
		t.Fatal(id, err)
	}
}
