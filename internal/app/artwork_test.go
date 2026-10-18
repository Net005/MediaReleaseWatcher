package app

import (
	"net/url"
	"testing"
)

func TestArtworkURLRejectsSSRFAndRedirectTargets(t *testing.T) {
	for _, raw := range []string{"http://image.tmdb.org/a.jpg", "https://127.0.0.1/a.jpg", "https://image.tmdb.org.evil.test/a.jpg", "https://image.tmdb.org:8443/a.jpg", "https://user:password@image.tmdb.org/a.jpg", "https://images.mdblist.com@evil.test/a.jpg", "file:///etc/passwd"} {
		u, _ := url.Parse(raw)
		if validateArtworkURL(u) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	u, _ := url.Parse("https://image.tmdb.org/t/p/w500/a.jpg")
	if e := validateArtworkURL(u); e != nil {
		t.Fatal(e)
	}
}
