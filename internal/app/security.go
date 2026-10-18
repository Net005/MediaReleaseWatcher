package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

func newCipher(secret string) (cipher.AEAD, error) {
	if len(secret) < 32 {
		return nil, errors.New("APP_SECRET must contain at least 32 random characters")
	}
	key := sha256.Sum256([]byte(secret))
	b, e := aes.NewCipher(key[:])
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func seal(c cipher.AEAD, plain []byte) ([]byte, error) {
	nonce := make([]byte, c.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return nil, e
	}
	return c.Seal(nonce, nonce, plain, []byte("mrw.settings.v1")), nil
}
func unseal(c cipher.AEAD, b []byte) ([]byte, error) {
	if len(b) < c.NonceSize() {
		return nil, errors.New("invalid encrypted settings")
	}
	return c.Open(nil, b[:c.NonceSize()], b[c.NonceSize():], []byte("mrw.settings.v1"))
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func tokenHash(s string) []byte { v := sha256.Sum256([]byte(s)); return v[:] }

var numericID = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
var imdbID = regexp.MustCompile(`^tt[0-9]{5,12}$`)

func personID(s string) (string, error) {
	if numericID.MatchString(s) {
		return s, nil
	}
	if strings.HasPrefix(s, "https://mdblist.com/actor/") {
		u, e := url.Parse(s)
		if e == nil && u.Host == "mdblist.com" && u.RawQuery == "" && u.Fragment == "" {
			id := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/actor/"), "/")
			if numericID.MatchString(id) {
				return id, nil
			}
		}
	}
	return "", errors.New("person ID must be a literal positive numeric TMDb ID or https://mdblist.com/actor/ID/ URL; malformed tokens are not normalized")
}
