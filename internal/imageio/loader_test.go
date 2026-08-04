package imageio

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testPNG builds a real PNG so the loader's decode check is exercised for real.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func newTestLoader() *Loader {
	l := NewLoader(2 * time.Second)
	return l
}

// ---------------------------------------------------------------------------
// basics
// ---------------------------------------------------------------------------

func TestLoadEmptySource(t *testing.T) {
	if _, err := newTestLoader().Load("   "); err != ErrEmptySource {
		t.Errorf("err = %v, want ErrEmptySource", err)
	}
}

func TestLoadUnrecognisedSource(t *testing.T) {
	_, err := newTestLoader().Load("just some words")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "unrecognised image_source") {
		t.Errorf("err = %v, want a clear 'unrecognised' message", err)
	}
}

func TestLoadRejectsNonImageBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "not-an-image.png")
	if err := os.WriteFile(p, []byte("this is plain text, not a PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newTestLoader().Load(p)
	if err == nil {
		t.Fatal("garbage must fail here, not inside the VLM call")
	}
	if !strings.Contains(err.Error(), "not a decodable image") {
		t.Errorf("err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// file paths
// ---------------------------------------------------------------------------

func TestLoadAbsolutePath(t *testing.T) {
	data := testPNG(t, 40, 20)
	p := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}

	img, err := newTestLoader().Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Source != SourceFile {
		t.Errorf("Source = %q, want %q", img.Source, SourceFile)
	}
	if img.Width != 40 || img.Height != 20 {
		t.Errorf("dimensions = %dx%d, want 40x20", img.Width, img.Height)
	}
	if img.Format != "png" {
		t.Errorf("Format = %q", img.Format)
	}
	if img.ID != Fingerprint(data) {
		t.Errorf("ID = %q, want the MD5 of the bytes", img.ID)
	}
}

func TestLoadRelativePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rel.png"), testPNG(t, 8, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	img, err := newTestLoader().Load("rel.png")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Source != SourceFile {
		t.Errorf("Source = %q", img.Source)
	}
}

func TestLoadFileURI(t *testing.T) {
	data := testPNG(t, 12, 12)
	p := filepath.Join(t.TempDir(), "b.png")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}

	uri := "file://"
	if runtime.GOOS == "windows" {
		uri += "/" + filepath.ToSlash(p)
	} else {
		uri += p
	}
	// url.PathEscape would break the drive colon; only escape spaces.
	uri = strings.ReplaceAll(uri, " ", "%20")

	img, err := newTestLoader().Load(uri)
	if err != nil {
		t.Fatalf("Load(%q): %v", uri, err)
	}
	if img.Source != SourceFile {
		t.Errorf("Source = %q", img.Source)
	}
	if img.Width != 12 {
		t.Errorf("Width = %d", img.Width)
	}
}

func TestLoadMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.png")
	if _, err := newTestLoader().Load(p); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

// ---------------------------------------------------------------------------
// data URI and base64
// ---------------------------------------------------------------------------

func TestLoadDataURI(t *testing.T) {
	data := testPNG(t, 16, 16)
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)

	img, err := newTestLoader().Load(uri)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Source != SourceDataURI {
		t.Errorf("Source = %q, want %q", img.Source, SourceDataURI)
	}
	if img.ID != Fingerprint(data) {
		t.Error("fingerprint mismatch: the decoded bytes differ from the input")
	}
	// The origin must not embed the whole payload.
	if len(img.Origin) > 40 {
		t.Errorf("Origin = %q, want something short", img.Origin)
	}
}

func TestLoadDataURIRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"data:image/png;base64,!!!!not-base64!!!!",
		"data:image/png,plain-text-not-encoded",
	} {
		if _, err := newTestLoader().Load(s); err == nil {
			t.Errorf("want an error for %q", s)
		}
	}
}

func TestLoadBareBase64(t *testing.T) {
	data := testPNG(t, 32, 32)
	enc := base64.StdEncoding.EncodeToString(data)
	if len(enc) < 64 {
		t.Fatal("test payload too small to trigger the base64 branch")
	}

	img, err := newTestLoader().Load(enc)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Source != SourceBase64 {
		t.Errorf("Source = %q, want %q", img.Source, SourceBase64)
	}
	if img.Width != 32 {
		t.Errorf("Width = %d", img.Width)
	}
}

// Agents routinely paste base64 with embedded newlines.
func TestLoadBase64WithWhitespace(t *testing.T) {
	data := testPNG(t, 32, 32)
	enc := base64.StdEncoding.EncodeToString(data)
	var wrapped strings.Builder
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		wrapped.WriteString(enc[i:end])
		wrapped.WriteString("\n")
	}

	img, err := newTestLoader().Load(wrapped.String())
	if err != nil {
		t.Fatalf("wrapped base64 rejected: %v", err)
	}
	if img.ID != Fingerprint(data) {
		t.Error("fingerprint mismatch after unwrapping")
	}
}

// A short word must be reported as "unrecognised", not as a base64 failure.
func TestShortStringIsNotTreatedAsBase64(t *testing.T) {
	_, err := newTestLoader().Load("abcdef")
	if err == nil || !strings.Contains(err.Error(), "unrecognised") {
		t.Errorf("err = %v, want an 'unrecognised' message", err)
	}
}

// ---------------------------------------------------------------------------
// image_id / L0 archive
// ---------------------------------------------------------------------------

func TestLoadImageIDFromArchive(t *testing.T) {
	data := testPNG(t, 24, 24)
	id := Fingerprint(data)

	l := newTestLoader()
	l.Archive = func(want string) ([]byte, bool) {
		if want == id {
			return data, true
		}
		return nil, false
	}

	img, err := l.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Source != SourceArchive {
		t.Errorf("Source = %q, want %q", img.Source, SourceArchive)
	}
	if img.ID != id {
		t.Errorf("ID = %q, want %q", img.ID, id)
	}
}

func TestLoadImageIDIsCaseInsensitive(t *testing.T) {
	data := testPNG(t, 8, 8)
	id := Fingerprint(data)

	l := newTestLoader()
	var asked string
	l.Archive = func(want string) ([]byte, bool) {
		asked = want
		return data, true
	}
	if _, err := l.Load(strings.ToUpper(id)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if asked != id {
		t.Errorf("archive was asked for %q, want the lowercase form %q", asked, id)
	}
}

func TestLoadImageIDWithoutArchive(t *testing.T) {
	id := Fingerprint([]byte("whatever"))
	_, err := newTestLoader().Load(id)
	if err == nil || !strings.Contains(err.Error(), "no archive") {
		t.Errorf("err = %v, want a clear 'no archive configured' message", err)
	}
}

func TestLoadImageIDMiss(t *testing.T) {
	l := newTestLoader()
	l.Archive = func(string) ([]byte, bool) { return nil, false }
	_, err := l.Load(Fingerprint([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "not found in archive") {
		t.Errorf("err = %v, want a recovery hint", err)
	}
}

func TestIsImageID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"d41d8cd98f00b204e9800998ecf8427e", true},
		{"D41D8CD98F00B204E9800998ECF8427E", true},
		{"  d41d8cd98f00b204e9800998ecf8427e  ", true},
		{"d41d8cd98f00b204e9800998ecf8427", false},   // 31 chars
		{"d41d8cd98f00b204e9800998ecf8427ee", false}, // 33 chars
		{"g41d8cd98f00b204e9800998ecf8427e", false},  // non-hex
		{"", false},
	}
	for _, tc := range cases {
		if got := IsImageID(tc.in); got != tc.want {
			t.Errorf("IsImageID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

func TestLoadHTTP(t *testing.T) {
	data := testPNG(t, 64, 48)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "llm-eyes-mcp") {
			t.Errorf("User-Agent = %q", got)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	img, err := newTestLoader().Load(srv.URL + "/pic.png")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Source != SourceHTTP {
		t.Errorf("Source = %q", img.Source)
	}
	if img.Width != 64 || img.Height != 48 {
		t.Errorf("dimensions = %dx%d", img.Width, img.Height)
	}
}

// 4xx will not fix itself, so it must not be retried.
func TestLoadHTTP404DoesNotRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestLoader().Load(srv.URL + "/missing.png")
	if err == nil {
		t.Fatal("want an error")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server was hit %d times, want 1 (4xx must not be retried)", n)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want the status code", err)
	}
}

// 5xx is transient, so it must be retried.
func TestLoadHTTP500Retries(t *testing.T) {
	var hits int32
	data := testPNG(t, 8, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	img, err := newTestLoader().Load(srv.URL + "/flaky.png")
	if err != nil {
		t.Fatalf("Load after retries: %v", err)
	}
	if img.Width != 8 {
		t.Errorf("Width = %d", img.Width)
	}
	if n := atomic.LoadInt32(&hits); n != 3 {
		t.Errorf("hits = %d, want 3", n)
	}
}

func TestLoadHTTPErrorBodyIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("x", 10000)))
	}))
	defer srv.Close()

	_, err := newTestLoader().Load(srv.URL + "/big-error.png")
	if err == nil {
		t.Fatal("want an error")
	}
	if len(err.Error()) > 500 {
		t.Errorf("error message is %d chars - it would blow up the agent's context", len(err.Error()))
	}
}

func TestLoadHTTPRespectsMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(testPNG(t, 200, 200))
	}))
	defer srv.Close()

	l := newTestLoader()
	l.MaxBytes = 128 // deliberately tiny
	if _, err := l.Load(srv.URL + "/big.png"); err == nil {
		t.Fatal("oversized download was accepted")
	}
}

// The query string frequently carries a signed token; it must never be logged.
func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://cdn.example.com/a.png", "https://cdn.example.com/a.png"},
		{"https://cdn.example.com/a.png?sig=SECRET&exp=1", "https://cdn.example.com/a.png?<redacted>"},
		{"https://cdn.example.com/a.png#frag", "https://cdn.example.com/a.png?<redacted>"},
	}
	for _, tc := range cases {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoadHTTPErrorDoesNotLeakTheQueryString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := newTestLoader().Load(srv.URL + "/a.png?token=SUPERSECRET")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("signed token leaked into the error: %v", err)
	}
}

func TestLoadHTTPRedirectCap(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusFound) // infinite loop
	}))
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := newTestLoader().Load(srv.URL + "/loop.png"); err == nil {
			t.Error("an infinite redirect chain was accepted")
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("redirect loop was not capped")
	}
}

func TestHTTPClientAlwaysHasATimeout(t *testing.T) {
	if got := NewLoader(0).HTTPClient.Timeout; got <= 0 {
		t.Error("a zero timeout means 'wait forever', which hangs the MCP client")
	}
	if got := NewLoader(7 * time.Second).HTTPClient.Timeout; got != 7*time.Second {
		t.Errorf("Timeout = %v, want 7s", got)
	}
}

// ---------------------------------------------------------------------------
// pure helpers
// ---------------------------------------------------------------------------

func TestMIMETypeFromURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a.png", "image/png"},
		{"a.PNG", "image/png"},
		{"https://x.com/a.jpg", "image/jpeg"},
		{"https://x.com/a.jpeg?width=100", "image/jpeg"},
		{"https://x.com/a.WEBP#anchor", "image/webp"},
		{"a.gif", "image/gif"},
		{"a.bmp", "image/bmp"},
		{"a.tiff", "image/tiff"},
		{"a.tif", "image/tiff"},
		{"noextension", "application/octet-stream"},
		{"", "application/octet-stream"},
		{"https://x.com/download?file=a.png&t=1", "application/octet-stream"}, // extension is in the query
	}
	for _, tc := range cases {
		if got := MIMETypeFromURL(tc.in); got != tc.want {
			t.Errorf("MIMETypeFromURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMIMETypeFromFormat(t *testing.T) {
	cases := []struct{ in, want string }{
		{"png", "image/png"},
		{"PNG", "image/png"},
		{"jpeg", "image/jpeg"},
		{"webp", "image/webp"},
		{"gif", "image/gif"},
		{"bmp", "image/bmp"},
		{"tiff", "image/tiff"},
		{"heic", "application/octet-stream"},
		{"", "application/octet-stream"},
	}
	for _, tc := range cases {
		if got := MIMETypeFromFormat(tc.in); got != tc.want {
			t.Errorf("MIMETypeFromFormat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Truncation must be rune-based: cutting a multi-byte character in half
// produces invalid UTF-8 in the agent's context.
func TestTruncateIsRuneSafe(t *testing.T) {
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("short string was modified: %q", got)
	}
	got := Truncate("日本語のテキストがここにあります", 5)
	if !strings.HasPrefix(got, "日本語のテ") {
		t.Errorf("got %q, want the first 5 runes", got)
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Errorf("got %q, want a truncation marker", got)
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("truncation produced a replacement character: %q", got)
		}
	}
}

func TestFingerprintIsStable(t *testing.T) {
	data := []byte("hello world")
	a, b := Fingerprint(data), Fingerprint(data)
	if a != b {
		t.Error("fingerprint is not deterministic")
	}
	if len(a) != 32 {
		t.Errorf("length = %d, want 32", len(a))
	}
	if a == Fingerprint([]byte("hello worle")) {
		t.Error("one-byte change did not move the fingerprint")
	}
}

func TestStripWhitespace(t *testing.T) {
	if got := stripWhitespace(" a\tb\r\nc "); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
}

func TestFileURLToPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		got, err := fileURLToPath("file:///C:/tmp/a.png")
		if err != nil {
			t.Fatalf("fileURLToPath: %v", err)
		}
		if !strings.Contains(strings.ToLower(got), "tmp") {
			t.Errorf("path = %q", got)
		}
	} else {
		got, err := fileURLToPath("file:///tmp/a.png")
		if err != nil {
			t.Fatalf("fileURLToPath: %v", err)
		}
		if got != "/tmp/a.png" {
			t.Errorf("path = %q, want /tmp/a.png", got)
		}
	}

	if _, err := fileURLToPath("file://" + string([]byte{0x7f}) + "%zz"); err == nil {
		t.Log("malformed file URI accepted (tolerated)")
	}
	_ = url.URL{}
}
