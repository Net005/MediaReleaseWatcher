package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func coverPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 2, 3))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPushoverMultipartBinaryCoverAndContentURL(t *testing.T) {
	cover := coverPNG(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/1/messages.json" || r.URL.RawQuery != "" {
			t.Errorf("unexpected Pushover request: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(6 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		expected := map[string]string{"token": "synthetic-push-token", "user": "synthetic-push-user", "title": "Synthetic release", "message": "Certification: PG-13; suggested age: unknown; content: unknown.", "url": "https://watcher.example/releases/42", "url_title": "View content details", "device": "synthetic-phone"}
		for k, want := range expected {
			if r.FormValue(k) != want {
				t.Errorf("field %s: %q", k, r.FormValue(k))
			}
		}
		file, header, err := r.FormFile("attachment")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		got, _ := io.ReadAll(file)
		if !bytes.Equal(got, cover) || header.Filename != "cover.png" || header.Header.Get("Content-Type") != "image/png" {
			t.Errorf("attachment: %+v len=%d", header, len(got))
		}
		if r.FormValue("attachment_base64") != "" {
			t.Error("not binary multipart")
		}
		w.Write([]byte(`{"status":1,"request":"synthetic"}`))
	}))
	defer s.Close()
	c := NewClient(Config{PushoverToken: "synthetic-push-token", PushoverUser: "synthetic-push-user", PushoverURL: s.URL + "/1/messages.json"})
	err := c.Notify(context.Background(), Notification{Title: "Synthetic release", Message: "Certification: PG-13; suggested age: unknown; content: unknown.", URL: "https://watcher.example/releases/42", URLTitle: "View content details", Device: "synthetic-phone", AttachmentName: "cover.png", Attachment: cover})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPushoverHTTP200StatusZeroIsFailureAndSanitized(t *testing.T) {
	for _, response := range []string{`{"status":0,"errors":["synthetic-push-token synthetic-push-user"]}`, `{"request":"no-status"}`, `null`, `{"status":"1"}`} {
		t.Run(response, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(response)) }))
			defer s.Close()
			c := NewClient(Config{PushoverToken: "synthetic-push-token", PushoverUser: "synthetic-push-user", PushoverURL: s.URL})
			err := c.Notify(context.Background(), Notification{Message: "Synthetic notification"})
			if err == nil || strings.Contains(err.Error(), "synthetic-push-token") || strings.Contains(err.Error(), "synthetic-push-user") {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}

func TestPushoverRejectsOversizeOrInvalidNotificationBeforeSend(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]int{"status": 1})
	}))
	defer s.Close()
	c := NewClient(Config{PushoverToken: "token", PushoverUser: "user", PushoverURL: s.URL})
	for _, n := range []Notification{{}, {Message: "hi", Attachment: make([]byte, 5_242_881)}, {Message: "hi", Attachment: []byte("not an image")}, {Message: "hi", URL: "javascript:alert(1)"}, {Message: strings.Repeat("x", 1025)}, {Message: "hi", Title: strings.Repeat("x", 251)}, {Message: "hi", URLTitle: strings.Repeat("x", 101)}} {
		if err := c.Notify(context.Background(), n); err == nil {
			t.Error("accepted invalid notification")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid payloads sent %d requests", calls.Load())
	}
	for _, cfg := range []Config{{}, {PushoverToken: "token"}, {PushoverUser: "user"}} {
		if err := NewClient(cfg).Notify(context.Background(), Notification{Message: "hi"}); err == nil {
			t.Error("accepted missing credentials")
		}
	}
}
