package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif" // decoders for preview thumbnails
	"image/jpeg"
	_ "image/png"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/queue"
	"github.com/douglasdf99/litezap/internal/store"
	"golang.org/x/net/html"
)

// previewBudget bounds the whole preview (page and thumbnail): past it the message goes
// without a preview (spec M6: never more than 5s). A variable for tests.
var previewBudget = 5 * time.Second

const (
	maxPreviewHTML   = 512 << 10
	maxPreviewImage  = 2 << 20
	maxPreviewPixels = 4096 * 4096
	thumbSide        = 300
	maxPreviewTitle  = 300
	maxPreviewDesc   = 1000
)

// LinkPreviewInput is the link_preview field of a text: "auto" (read the first URL of the
// text), "off" (default), or an object with a custom card.
type LinkPreviewInput struct {
	Mode        string `json:"-"` // auto | off | custom
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
}

func (l *LinkPreviewInput) UnmarshalJSON(b []byte) error {
	var mode string
	if err := json.Unmarshal(b, &mode); err == nil {
		if mode != "auto" && mode != "off" {
			return errors.New(`link_preview must be "auto", "off" or an object`)
		}
		*l = LinkPreviewInput{Mode: mode}
		return nil
	}
	type plain LinkPreviewInput
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return errors.New(`link_preview must be "auto", "off" or an object`)
	}
	*l = LinkPreviewInput(p)
	l.Mode = "custom"
	return nil
}

func (l LinkPreviewInput) MarshalJSON() ([]byte, error) {
	if l.Mode == "auto" || l.Mode == "off" {
		return json.Marshal(l.Mode)
	}
	type plain LinkPreviewInput
	return json.Marshal(plain(l))
}

var urlInText = regexp.MustCompile(`https?://[^\s<>"'\x60]+`)

// firstURL returns the first http(s) URL of a text, without trailing punctuation.
func firstURL(text string) string {
	u := urlInText.FindString(text)
	return strings.TrimRight(u, ".,;:!?)]}*_~")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// pageMeta is what a page says about itself.
type pageMeta struct {
	title, description, image string
}

// parsePage reads the Open Graph / Twitter / <title> metadata of an HTML document head.
func parsePage(doc []byte) pageMeta {
	var m pageMeta
	var title string
	z := html.NewTokenizer(bytes.NewReader(doc))
	inTitle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			goto done
		case html.TextToken:
			if inTitle && title == "" {
				title = string(z.Text())
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				inTitle = false
			case "head":
				goto done
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "title":
				inTitle = tt == html.StartTagToken
			case "body":
				goto done
			case "meta":
				var key, content string
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch strings.ToLower(string(k)) {
					case "property", "name":
						key = strings.ToLower(string(v))
					case "content":
						content = string(v)
					}
				}
				switch key {
				case "og:title":
					m.title = content
				case "twitter:title":
					if m.title == "" {
						m.title = content
					}
				case "og:description":
					m.description = content
				case "description", "twitter:description":
					if m.description == "" {
						m.description = content
					}
				case "og:image", "og:image:url", "og:image:secure_url":
					if m.image == "" {
						m.image = content
					}
				case "twitter:image":
					if m.image == "" {
						m.image = content
					}
				}
			}
		}
	}
done:
	if m.title == "" {
		m.title = title
	}
	m.title, m.description = clip(html.UnescapeString(m.title), maxPreviewTitle), clip(html.UnescapeString(m.description), maxPreviewDesc)
	return m
}

// thumbnail turns an image into a small JPEG; nil when it is not a decodable JPEG, PNG or
// GIF, or is too large to decode safely.
func thumbnail(data []byte) []byte {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPreviewPixels {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > thumbSide || h > thumbSide {
		if w >= h {
			w, h = thumbSide, max(1, h*thumbSide/b.Dx())
		} else {
			w, h = max(1, w*thumbSide/b.Dy()), thumbSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ { // nearest neighbour: good enough for a preview card
		sy := b.Min.Y + y*b.Dy()/h
		for x := 0; x < w; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*b.Dx()/w, sy))
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 70}); err != nil {
		return nil
	}
	return out.Bytes()
}

// validPreviewURL accepts an absolute http(s) URL.
func validPreviewURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && len(raw) <= 2048
}

// linkPreview builds the preview card of a text. Validation errors are returned; network
// failures are not (the message simply goes without a preview, or with the custom
// fields only). nil: no preview.
func (s *Messaging) linkPreview(ctx context.Context, in *LinkPreviewInput, text string) (*queue.LinkPreview, *store.LinkPreview, error) {
	if in == nil || in.Mode == "off" || in.Mode == "" {
		return nil, nil, nil
	}
	target := in.URL
	if target == "" {
		target = firstURL(text)
	}
	if target == "" {
		if in.Mode == "auto" {
			return nil, nil, nil // nothing to preview
		}
		return nil, nil, apierr.Validation("link_preview.url is required when the text has no URL")
	}
	if !validPreviewURL(target) {
		return nil, nil, apierr.Validation("link_preview.url must be an absolute http(s) URL")
	}
	if in.ImageURL != "" && !validPreviewURL(in.ImageURL) {
		return nil, nil, apierr.Validation("link_preview.image_url must be an absolute http(s) URL")
	}
	if len(in.Title) > maxPreviewTitle || len(in.Description) > maxPreviewDesc {
		return nil, nil, apierr.Validation("link_preview title is limited to 300 bytes and description to 1000")
	}
	ctx, cancel := context.WithTimeout(ctx, previewBudget)
	defer cancel()
	lp := &queue.LinkPreview{URL: target, Title: in.Title, Description: in.Description}
	image := in.ImageURL
	if in.Mode == "auto" {
		doc, final, err := s.Fetcher.FetchPage(ctx, target, maxPreviewHTML)
		if err != nil {
			return nil, nil, nil // private address, timeout, not HTML...: no preview
		}
		pm := parsePage(doc)
		if pm.title == "" && pm.description == "" {
			return nil, nil, nil
		}
		lp.Title, lp.Description = pm.title, pm.description
		if pm.image != "" {
			if ref, err := final.Parse(pm.image); err == nil && validPreviewURL(ref.String()) {
				image = ref.String()
			}
		}
	}
	if image != "" {
		if data, _, err := s.Fetcher.Fetch(ctx, image, maxPreviewImage); err == nil {
			lp.Thumbnail = thumbnail(data)
		}
	}
	if lp.Title == "" && lp.Description == "" && lp.Thumbnail == nil {
		return nil, nil, nil
	}
	return lp, &store.LinkPreview{URL: lp.URL, Title: lp.Title, Description: lp.Description}, nil
}
