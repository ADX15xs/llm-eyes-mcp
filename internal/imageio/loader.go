// Package imageio implements the universal image loader: agents pass images in
// wildly different shapes (URL, path, data URI, bare base64, or a previously
// returned image_id) and this package normalises all of them into raw bytes
// plus a content-addressed MD5 identifier.
package imageio

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	// Register decoders for format sniffing. Blank imports only - we never
	// re-encode here.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// DefaultMaxBytes caps how much data the loader will accept from any source.
const DefaultMaxBytes = 32 << 20 // 32 MiB

// Source classifies where an image came from (useful for logs and tests).
type Source string

const (
	SourceHTTP    Source = "http"
	SourceFile    Source = "file"
	SourceDataURI Source = "data-uri"
	SourceBase64  Source = "base64"
	SourceArchive Source = "archive" // resolved from an image_id via L0
)

// Image is a fully loaded, content-addressed image.
type Image struct {
	// ID is the MD5 of Data in lowercase hex. It is the primary cache key and
	// the handle agents pass back on subsequent turns.
	ID     string
	Data   []byte
	Format string // "png", "jpeg", "webp", ...
	Width  int
	Height int
	Source Source
	// Origin is a redacted description of the input, safe to log.
	Origin string
}

// ArchiveLookup resolves an image_id to previously archived bytes (L0).
type ArchiveLookup func(imageID string) ([]byte, bool)

// Loader turns an arbitrary image_source string into an Image.
type Loader struct {
	HTTPClient *http.Client
	MaxBytes   int64
	// Archive lets bare image_id inputs resolve against the L0 archive.
	Archive ArchiveLookup
}

// NewLoader builds a loader with a bounded HTTP client. A zero-value
// http.Client has no timeout, which would hang the MCP client forever.
func NewLoader(timeout time.Duration) *Loader {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Loader{
		HTTPClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("stopped after 5 redirects")
				}
				return nil
			},
		},
		MaxBytes: DefaultMaxBytes,
	}
}

var (
	imageIDRe = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
	winPathRe = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)
	base64Re  = regexp.MustCompile(`^[A-Za-z0-9+/\r\n\s]+={0,2}$`)
	dataURIRe = regexp.MustCompile(`^data:([^;,]*)(;[^,]*)?,`)
)

// ErrEmptySource is returned when image_source is blank.
var ErrEmptySource = errors.New("image_source is empty")

// Load sniffs the protocol of src and returns the decoded image.
func (l *Loader) Load(src string) (*Image, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return nil, ErrEmptySource
	}

	switch {
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		data, err := l.fetchHTTP(s)
		if err != nil {
			return nil, err
		}
		return finalize(data, SourceHTTP, redactURL(s))

	case strings.HasPrefix(s, "data:"):
		data, err := decodeDataURI(s)
		if err != nil {
			return nil, err
		}
		return finalize(data, SourceDataURI, "data-uri")

	case strings.HasPrefix(s, "file://"):
		p, err := fileURLToPath(s)
		if err != nil {
			return nil, err
		}
		data, err := l.readFile(p)
		if err != nil {
			return nil, err
		}
		return finalize(data, SourceFile, p)

	case imageIDRe.MatchString(s):
		if l.Archive == nil {
			return nil, fmt.Errorf("image_source looks like an image_id (%s) but no archive is configured", s)
		}
		data, ok := l.Archive(strings.ToLower(s))
		if !ok {
			return nil, fmt.Errorf("image_id %s not found in archive; pass the original image once more", s)
		}
		return finalize(data, SourceArchive, s)

	case strings.HasPrefix(s, "/"), winPathRe.MatchString(s), strings.HasPrefix(s, `\\`):
		data, err := l.readFile(s)
		if err != nil {
			return nil, err
		}
		return finalize(data, SourceFile, s)
	}

	// Fall back to bare base64. Guard on length so a stray word is reported as
	// an unrecognised source rather than a base64 decode failure.
	if len(s) >= 64 && base64Re.MatchString(s) {
		clean := stripWhitespace(s)
		data, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			if data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(clean, "=")); err != nil {
				return nil, fmt.Errorf("decode base64 image_source: %w", err)
			}
		}
		return finalize(data, SourceBase64, "base64")
	}

	// Last chance: a relative path that actually exists.
	if _, err := os.Stat(s); err == nil {
		data, err := l.readFile(s)
		if err != nil {
			return nil, err
		}
		return finalize(data, SourceFile, s)
	}

	return nil, fmt.Errorf("unrecognised image_source %q: expected http(s) URL, file path, file:// URI, data URI, base64, or a 32-char image_id",
		truncate(s, 60))
}

func (l *Loader) maxBytes() int64 {
	if l.MaxBytes > 0 {
		return l.MaxBytes
	}
	return DefaultMaxBytes
}

func (l *Loader) fetchHTTP(rawURL string) ([]byte, error) {
	client := l.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "llm-eyes-mcp/1.0")

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("fetch %s: %w", redactURL(rawURL), err)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, l.maxBytes()+1))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("fetch %s: HTTP %d: %s",
				redactURL(rawURL), resp.StatusCode, truncate(string(body), 200))
			// 4xx will not fix itself; stop retrying.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return nil, lastErr
			}
			continue
		}
		if readErr != nil {
			lastErr = fmt.Errorf("read body from %s: %w", redactURL(rawURL), readErr)
			continue
		}
		if int64(len(body)) > l.maxBytes() {
			return nil, fmt.Errorf("image from %s exceeds %d bytes", redactURL(rawURL), l.maxBytes())
		}
		return body, nil
	}
	return nil, lastErr
}

func (l *Loader) readFile(path string) ([]byte, error) {
	clean := filepath.Clean(path)
	st, err := os.Stat(clean)
	if err != nil {
		return nil, fmt.Errorf("read image file %s: %w", clean, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("read image file %s: is a directory", clean)
	}
	if st.Size() > l.maxBytes() {
		return nil, fmt.Errorf("image file %s is %d bytes, exceeds limit %d", clean, st.Size(), l.maxBytes())
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return nil, fmt.Errorf("read image file %s: %w", clean, err)
	}
	return data, nil
}

func decodeDataURI(s string) ([]byte, error) {
	m := dataURIRe.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("malformed data URI: missing comma separator")
	}
	payload := s[len(m[0]):]
	if strings.Contains(m[2], "base64") {
		data, err := base64.StdEncoding.DecodeString(stripWhitespace(payload))
		if err != nil {
			return nil, fmt.Errorf("decode data URI base64: %w", err)
		}
		return data, nil
	}
	// Percent-encoded payload (rare but legal).
	decoded, err := url.QueryUnescape(payload)
	if err != nil {
		return nil, fmt.Errorf("decode data URI payload: %w", err)
	}
	return []byte(decoded), nil
}

func fileURLToPath(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse file URI: %w", err)
	}
	p := u.Path
	if p == "" {
		p = u.Opaque
	}
	// file:///C:/x/y.png -> /C:/x/y.png ; strip the leading slash on Windows.
	if winPathRe.MatchString(strings.TrimPrefix(p, "/")) {
		p = strings.TrimPrefix(p, "/")
	}
	if p == "" {
		return "", fmt.Errorf("file URI has no path: %s", s)
	}
	return filepath.FromSlash(p), nil
}

// finalize computes the MD5 fingerprint and verifies the bytes really decode as
// an image, so garbage input fails here rather than inside the VLM call.
func finalize(data []byte, src Source, origin string) (*Image, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("image from %s is empty", src)
	}
	cfg, format, err := image.DecodeConfig(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("input from %s is not a decodable image: %w", src, err)
	}
	sum := md5.Sum(data)
	return &Image{
		ID:     hex.EncodeToString(sum[:]),
		Data:   data,
		Format: format,
		Width:  cfg.Width,
		Height: cfg.Height,
		Source: src,
		Origin: origin,
	}, nil
}

// Fingerprint returns the lowercase hex MD5 of data.
func Fingerprint(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// IsImageID reports whether s looks like an MD5 image identifier.
func IsImageID(s string) bool { return imageIDRe.MatchString(strings.TrimSpace(s)) }

func stripWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
}

// redactURL removes query parameters, which frequently carry signed tokens.
func redactURL(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i] + "?<redacted>"
	}
	return raw
}

// truncate shortens s to max runes. Rune-based (not byte-based) so multi-byte
// text is never cut into an invalid half-character.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "...[truncated]"
}

// Truncate is the exported form of truncate, shared by other packages.
func Truncate(s string, max int) string { return truncate(s, max) }

// MIMETypeFromURL guesses a MIME type from a URL or path. It handles query
// strings, fragments and uppercase extensions, and refuses to guess otherwise.
func MIMETypeFromURL(rawURL string) string {
	u := rawURL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.ToLower(u)
	switch {
	case strings.HasSuffix(u, ".png"):
		return "image/png"
	case strings.HasSuffix(u, ".jpg"), strings.HasSuffix(u, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(u, ".webp"):
		return "image/webp"
	case strings.HasSuffix(u, ".gif"):
		return "image/gif"
	case strings.HasSuffix(u, ".bmp"):
		return "image/bmp"
	case strings.HasSuffix(u, ".tif"), strings.HasSuffix(u, ".tiff"):
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}

// MIMETypeFromFormat maps a decoder format name to a MIME type.
func MIMETypeFromFormat(format string) string {
	switch strings.ToLower(format) {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "bmp":
		return "image/bmp"
	case "tiff":
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}
