package fileviewer

import (
	"bytes"
	"strings"
	"testing"
)

func TestDetectMIME_MagicBytes(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"pdf", []byte("%PDF-1.7\n..."), "application/pdf"},
		{"png", append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...), "image/png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), "image/jpeg"},
		{"gif87a", []byte("GIF87a....."), "image/gif"},
		{"gif89a", []byte("GIF89a....."), "image/gif"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		{"bmp", []byte("BM\x00\x00\x00\x00"), "image/bmp"},
		{"mp4", []byte("\x00\x00\x00\x18ftypmp42"), "video/mp4"},
		{"ogg", []byte("OggS\x00\x02"), "audio/ogg"},
		{"flac", []byte("fLaC\x00\x00\x00\x22"), "audio/flac"},
		{"id3mp3", []byte("ID3\x04\x00\x00\x00"), "audio/mpeg"},
		{"wav", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "audio/wav"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectMagic(tc.head); got != tc.want {
				t.Fatalf("detectMagic(%q) = %q, want %q", tc.head[:8], got, tc.want)
			}
		})
	}
}

func TestDetectMIME_TextFallbacks(t *testing.T) {
	// Plain text with no signature → text/plain.
	if got := DetectMIME([]byte("hello world\n"), "notes.txt", ""); got != "text/plain" {
		t.Fatalf("plain text = %q", got)
	}
	// JSON body without extension → text/plain from the generic sniffer;
	// with .json extension the magic tier still misses but the generic
	// sniffer wins first — assert it is text-classified either way.
	got := DetectMIME([]byte(`{"a":1}`), "data", "")
	if !IsTextFormat(got) {
		t.Fatalf("json body should classify as text, got %q", got)
	}
	// Parameters stripped (EC-48).
	if got := stripMimeParams("text/html; charset=utf-8"); got != "text/html" {
		t.Fatalf("stripMimeParams = %q", got)
	}
}

func TestDetectMIME_ServerWinsOverDeclared(t *testing.T) {
	// PNG bytes declared as PDF → server detection wins (EC-16 / test 10).
	got := DetectMIME(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...), "fake.pdf", "application/pdf")
	if got != "image/png" {
		t.Fatalf("server detection must win; got %q", got)
	}
}

func TestNormalizeExtension(t *testing.T) {
	cases := map[string]string{
		"FILE.PDF":       "pdf", // EC-45
		"archive.tar.gz": "gz",  // EC-46
		"noext":          "",    // EC-47
		"Movie.MP4":      "mp4",
		"a.b.c.PNG":      "png",
	}
	for in, want := range cases {
		if got := NormalizeExtension(in); got != want {
			t.Fatalf("NormalizeExtension(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestViewerHintFor(t *testing.T) {
	cases := map[string]string{
		"application/pdf":          "pdf",
		"image/png":                "image",
		"text/csv":                 "csv",
		"application/json":         "json",
		"text/markdown":            "markdown",
		"video/mp4":                "audio_video",
		"text/x-go":                "code",
		"text/plain":               "code", // EC-23: unmapped text routes to code
		"application/octet-stream": "",     // EC-15: download-only
	}
	for mime, want := range cases {
		if got := ViewerHintFor(mime); got != want {
			t.Fatalf("ViewerHintFor(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestIsValidSHA256AndSlug(t *testing.T) {
	if !IsValidSHA256(strings.Repeat("a", 64)) {
		t.Fatal("64-hex should be valid")
	}
	if IsValidSHA256(strings.Repeat("A", 64)) || IsValidSHA256("abc") {
		t.Fatal("uppercase/short digests must be invalid")
	}
	for _, ok := range []string{"pdf", "audio_video", "code2"} {
		if !IsValidViewerSlug(ok) {
			t.Fatalf("%q should be a valid slug", ok)
		}
	}
	for _, bad := range []string{"", "Pdf", "2code", "has space", "has-dash"} {
		if IsValidViewerSlug(bad) {
			t.Fatalf("%q should be an invalid slug", bad)
		}
	}
}

func TestParseRangeHeader(t *testing.T) {
	const size = 1000

	// Absent header → full body.
	if br, err := parseRangeHeader("", size); br != nil || err != nil {
		t.Fatalf("empty header should give nil,nil; got %v %v", br, err)
	}

	cases := []struct {
		header string
		start  int64
		end    int64
	}{
		{"bytes=0-99", 0, 99},
		{"bytes=500-", 500, 999},
		{"bytes=-500", 500, 999}, // suffix: last 500 bytes
		{"bytes=100-199", 100, 199},
	}
	for _, tc := range cases {
		br, err := parseRangeHeader(tc.header, size)
		if err != nil {
			t.Fatalf("parseRangeHeader(%q): %v", tc.header, err)
		}
		if br.start != tc.start || br.end != tc.end {
			t.Fatalf("parseRangeHeader(%q) = %d-%d, want %d-%d", tc.header, br.start, br.end, tc.start, tc.end)
		}
		if br.length() != br.end-br.start+1 {
			t.Fatalf("length invariant broken for %q", tc.header)
		}
	}

	invalid := []string{"bytes", "chars=0-1", "bytes=0-1,5-6", "bytes=-", "bytes=a-b", "bytes=5-2"}
	for _, h := range invalid {
		if br, err := parseRangeHeader(h, size); err == nil || (err != ErrInvalidRangeHeader && br != nil) {
			t.Fatalf("parseRangeHeader(%q) should be ErrInvalidRangeHeader, got %v %v", h, br, err)
		}
	}

	unsatisfiable := []string{"bytes=1000-", "bytes=2000-3000", "bytes=0-1023"}
	for _, h := range unsatisfiable {
		if _, err := parseRangeHeader(h, size); err != ErrRangeNotSatisfiable {
			t.Fatalf("parseRangeHeader(%q) should be ErrRangeNotSatisfiable, got %v", h, err)
		}
	}
	// "bytes=-0" (zero-length suffix) is malformed, not unsatisfiable.
	if _, err := parseRangeHeader("bytes=-0", size); err != ErrInvalidRangeHeader {
		t.Fatalf("bytes=-0 should be ErrInvalidRangeHeader, got %v", err)
	}
}
