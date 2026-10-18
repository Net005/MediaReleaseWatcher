package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxArtwork = 5 << 20

func validateArtworkURL(u *url.URL) error {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || !oneOf(strings.ToLower(u.Hostname()), "image.tmdb.org", "images.mdblist.com", "mdblist.com") {
		return errors.New("artwork must be HTTPS on an approved public image host")
	}
	return nil
}
func imageInfo(b []byte) (string, error) {
	if len(b) > maxArtwork {
		return "", errors.New("image exceeds 5 MiB")
	}
	cfg, kind, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 20000 || cfg.Height > 20000 || int64(cfg.Width)*int64(cfg.Height) > 50_000_000 {
		return "", errors.New("invalid or excessively large image")
	}
	if _, _, e = image.Decode(bytes.NewReader(b)); e != nil {
		return "", errors.New("invalid or truncated image")
	}
	switch kind {
	case "jpeg":
		return "image/jpeg", nil
	case "png":
		return "image/png", nil
	case "gif":
		return "image/gif", nil
	case "webp":
		return "image/webp", nil
	}
	return "", errors.New("unsupported image")
}
func publicDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(addr)
	if e != nil {
		return nil, e
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil {
		return nil, errors.New("artwork DNS lookup failed")
	}
	for _, ip := range ips {
		if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
			return nil, errors.New("artwork DNS resolved to a non-public address")
		}
	}
	dial := net.Dialer{Timeout: 8 * time.Second}
	for _, ip := range ips {
		c, e := dial.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if e == nil {
			return c, nil
		}
	}
	return nil, errors.New("artwork host unavailable")
}
func (a *App) fetchArtwork(ctx context.Context, raw string) ([]byte, string, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return nil, "", errors.New("invalid artwork URL")
	}
	if e = validateArtworkURL(u); e != nil {
		return nil, "", e
	}
	hash := sha256.Sum256([]byte(raw))
	path := filepath.Join(a.cfg.DataDir, "artwork", hex.EncodeToString(hash[:]))
	if info, e := os.Stat(path); e == nil && info.Size() <= maxArtwork && time.Since(info.ModTime()) < 7*24*time.Hour {
		if b, e := os.ReadFile(path); e == nil {
			if mime, e := imageInfo(b); e == nil {
				return b, mime, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: publicDial, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too many artwork redirects")
		}
		return validateArtworkURL(req.URL)
	}}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return nil, "", errors.New("invalid artwork request")
	}
	req.Header.Set("Accept", "image/jpeg,image/png,image/webp,image/gif")
	res, e := client.Do(req)
	if e != nil {
		return nil, "", errors.New("artwork request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "image/") {
		return nil, "", errors.New("artwork server did not return an image")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, maxArtwork+1))
	if e != nil {
		return nil, "", errors.New("artwork read failed")
	}
	mime, e := imageInfo(b)
	if e != nil {
		return nil, "", e
	}
	if f, e := os.CreateTemp(filepath.Dir(path), "cover-*"); e == nil {
		name := f.Name()
		if _, e = f.Write(b); e == nil {
			e = f.Close()
			if e == nil {
				_ = os.Rename(name, path)
			}
		} else {
			_ = f.Close()
		}
		_ = os.Remove(name)
	}
	return b, mime, nil
}
func (a *App) artwork(w http.ResponseWriter, r *http.Request) {
	b, mime, e := a.fetchArtwork(r.Context(), r.URL.Query().Get("url"))
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private,max-age=86400")
	_, _ = w.Write(b)
}
