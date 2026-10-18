package integrations

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// Notify transmits caller-supplied content info and an optional binary image;
// it never downloads arbitrary URLs, infers content ratings, or retries sends.
func (c *Client) Notify(ctx context.Context, n Notification) error {
	if strings.TrimSpace(c.cfg.PushoverToken) == "" || strings.TrimSpace(c.cfg.PushoverUser) == "" {
		return errors.New("Pushover token and user key must be configured")
	}
	if strings.TrimSpace(n.Message) == "" || !utf8.ValidString(n.Message) || utf8.RuneCountInString(n.Message) > 1024 || utf8.RuneCountInString(n.Title) > 250 || utf8.RuneCountInString(n.URL) > 512 || utf8.RuneCountInString(n.URLTitle) > 100 || len(n.Device) > 1024 {
		return errors.New("Pushover message fields are empty or exceed limits")
	}
	if n.URL != "" {
		u, err := url.Parse(n.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return errors.New("Pushover click URL must be HTTP or HTTPS without credentials")
		}
	}
	if len(n.Attachment) > maxAttachmentBytes {
		return errors.New("Pushover image exceeds the 5 MB attachment limit")
	}
	target, err := endpoint(c.cfg.PushoverURL, "", nil)
	if err != nil {
		return err
	}
	imageType := ""
	if len(n.Attachment) > 0 {
		imageType = http.DetectContentType(n.Attachment)
		if imageType != "image/png" && imageType != "image/jpeg" && imageType != "image/gif" {
			return errors.New("Pushover attachment must be a PNG, JPEG or GIF image")
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(n.Attachment))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 10000 || cfg.Height > 10000 || int64(cfg.Width)*int64(cfg.Height) > 20_000_000 {
			return errors.New("Pushover attachment has invalid or excessive image dimensions")
		}
		if _, _, err := image.Decode(bytes.NewReader(n.Attachment)); err != nil {
			return errors.New("Pushover attachment is not a valid image")
		}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"token", c.cfg.PushoverToken}, {"user", c.cfg.PushoverUser}, {"title", n.Title}, {"message", n.Message}, {"url", n.URL}, {"url_title", n.URLTitle}, {"device", n.Device}} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return errors.New("Pushover multipart encoding failed")
		}
	}
	if len(n.Attachment) > 0 {
		filename := path.Base(strings.ReplaceAll(n.AttachmentName, "\\", "/"))
		if filename == "." || filename == "/" || filename == "" {
			filename = "cover"
		}
		if strings.ContainsAny(filename, "\r\n\x00") || len(filename) > 255 {
			return errors.New("invalid Pushover attachment filename")
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "attachment", "filename": filename}))
		header.Set("Content-Type", imageType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return errors.New("Pushover attachment encoding failed")
		}
		if _, err := part.Write(n.Attachment); err != nil {
			return errors.New("Pushover attachment encoding failed")
		}
	}
	if err := writer.Close(); err != nil {
		return errors.New("Pushover multipart encoding failed")
	}
	raw, err := c.request(ctx, "Pushover", http.MethodPost, target, "", writer.FormDataContentType(), body.Bytes())
	if err != nil {
		return err
	}
	var result struct {
		Status int `json:"status"`
	}
	if err := decode(raw, &result, "Pushover"); err != nil {
		return err
	}
	if result.Status != 1 {
		return errors.New("Pushover rejected the notification")
	}
	return nil
}
