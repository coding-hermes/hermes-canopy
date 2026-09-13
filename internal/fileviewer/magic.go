package fileviewer

import (
	"bytes"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

// MIME detection per SPEC-PL-02 §2: "MIME type → extension → magic-byte sniff.
// MIME is the primary signal; extension is the fallback for ambiguous files;
// magic bytes resolve text-vs-binary conflicts."
//
// Server-detected MIME always wins over the uploader's declaration; the
// declared value is preserved in file_metadata.declared_mime for audit
// (spec EC-16). Parameters are stripped (EC-48): "text/html; charset=utf-8"
// is stored as "text/html".

// maxSniff is how many leading bytes magic detection needs.
const maxSniff = 512

var sha256Re = regexp.MustCompile(`^[a-f0-9]{64}$`)

// IsValidSHA256 reports whether s is a lowercase 64-hex SHA-256 digest.
func IsValidSHA256(s string) bool { return sha256Re.MatchString(s) }

// IsValidViewerSlug reports whether s matches the registry's slug shape.
func IsValidViewerSlug(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false // must start with a letter
			}
		case r == '_':
		default:
			return false
		}
	}
	return true
}

// NormalizeExtension lowercases the filename's extension and strips the dot.
// "FILE.PDF" → "pdf" (EC-45); "archive.tar.gz" → "gz" (EC-46); a file with
// no extension yields "" (EC-47).
func NormalizeExtension(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	return strings.TrimPrefix(ext, ".")
}

// DetectMIME classifies content for storage in file_metadata.mime_type.
// Precedence: magic bytes → generic content sniff → extension for
// ambiguous text → declared MIME. Plain text is THE ambiguous case
// (§2: "extension is the fallback for ambiguous files"), so a `.csv`
// upload is refined to text/csv while unknown text stays text/plain.
// The result never carries parameters and is never empty.
func DetectMIME(head []byte, filename, declaredMime string) string {
	if detected := detectMagic(head); detected != "" {
		return detected
	}
	sniffed := stripMimeParams(http.DetectContentType(head))
	ext := NormalizeExtension(filename)
	if sniffed == "text/plain" {
		if m := mimeByExtension(ext); m != "" {
			return m
		}
		return sniffed
	}
	if sniffed != "" {
		return sniffed
	}
	if declared := stripMimeParams(declaredMime); declared != "" {
		return declared
	}
	if m := mimeByExtension(ext); m != "" {
		return m
	}
	return "application/octet-stream"
}

// stripMimeParams removes "; charset=..." style parameters (EC-48).
func stripMimeParams(m string) string {
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	return strings.TrimSpace(strings.ToLower(m))
}

// mimeByExtension is the extension fallback tier for files whose content
// carries no recognizable signature (plain text family).
func mimeByExtension(ext string) string {
	switch ext {
	case "md", "markdown", "mdx":
		return "text/markdown"
	case "csv":
		return "text/csv"
	case "tsv":
		return "text/tab-separated-values"
	case "json", "jsonc", "json5":
		return "application/json"
	case "html", "htm":
		return "text/html"
	case "css":
		return "text/css"
	case "xml":
		return "application/xml"
	case "yaml", "yml":
		return "text/x-yaml"
	case "toml":
		return "text/x-toml"
	default:
		return ""
	}
}

// detectMagic sniffs binary format signatures. The returned MIME (or "" for
// "no signature") is authoritative over every other signal.
func detectMagic(head []byte) string {
	// PDF (spec test 35: "%PDF-1.7" → application/pdf).
	if bytes.HasPrefix(head, []byte("%PDF-")) {
		return "application/pdf"
	}
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return "image/gif"
	case bytes.HasPrefix(head, []byte("RIFF")) && len(head) >= 12 && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp"
	case bytes.HasPrefix(head, []byte("BM")):
		return "image/bmp"
	case bytes.HasPrefix(head, []byte("\x00\x00\x00")) && len(head) >= 12 &&
		(bytes.Equal(head[4:8], []byte("ftypavif"))):
		return "image/avif"
	// MP4 family: size(4) 'ftyp' brand(4).
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		return "video/mp4"
	case bytes.HasPrefix(head, []byte("\x1a\x45\xdf\xa3")):
		// Matroska/WebM container: EBML header. Distinguish WebM via the
		// DocType element when present; otherwise treat as generic MKV/WebM
		// container, which the audio_video viewer handles.
		if bytes.Contains(head[:minInt(len(head), 64)], []byte("webm")) {
			return "video/webm"
		}
		return "video/webm"
	case bytes.HasPrefix(head, []byte("OggS")):
		return "audio/ogg"
	case bytes.HasPrefix(head, []byte("fLaC")):
		return "audio/flac"
	case bytes.HasPrefix(head, []byte("ID3")):
		return "audio/mpeg"
	case len(head) >= 2 && head[0] == 0xff && head[1]&0xe0 == 0xe0:
		// MPEG audio frame sync without an ID3 header.
		return "audio/mpeg"
	case bytes.HasPrefix(head, []byte("RIFF")) && len(head) >= 12 && bytes.Equal(head[8:12], []byte("WAVE")):
		return "audio/wav"
	case bytes.HasPrefix(head, []byte("{")), bytes.HasPrefix(head, []byte("[")):
		// Leading brace/bracket alone does not prove JSON; fall through to
		// the generic sniffer which will call it text.
		return ""
	}
	return ""
}

// IsTextFormat classifies a MIME type as text (drives is_text / is_binary /
// is_viewable on file_metadata). Spec §2: code, markdown, JSON, CSV are
// client-rendered from text; everything else is binary or download-only.
func IsTextFormat(mime string) bool {
	m := stripMimeParams(mime)
	switch {
	case strings.HasPrefix(m, "text/"):
		return true
	case m == "application/json" || m == "application/xml" ||
		m == "application/javascript" || m == "application/x-yaml" ||
		m == "application/toml":
		return true
	case strings.HasSuffix(m, "+json") || strings.HasSuffix(m, "+xml"):
		return true
	default:
		return false
	}
}

// IsImageFormat reports whether a MIME type is handled by the image viewer.
func IsImageFormat(mime string) bool {
	m := stripMimeParams(mime)
	return strings.HasPrefix(m, "image/")
}

// KnownViewerMIME reports whether DefaultViewerDispatch (or the code
// viewer's x- prefixed code languages) recognizes the MIME. Used to set
// is_viewable at insert time: files with no viewer fall through to
// download-only (EC-6, EC-15).
func KnownViewerMIME(mime string) bool {
	m := stripMimeParams(mime)
	if _, ok := DefaultViewerDispatch[m]; ok {
		return true
	}
	// Plain text is routed to the code viewer as a fallback (EC-23).
	return m == "text/plain"
}

// ViewerHintFor returns the server-detected viewer slug for a MIME type
// ("server-detected viewer_hint" column), or "" when no viewer applies.
func ViewerHintFor(mime string) string {
	m := stripMimeParams(mime)
	if slug, ok := DefaultViewerDispatch[m]; ok {
		return slug
	}
	if IsTextFormat(m) {
		// Unmapped text (plain text, unknown code language) routes to the
		// code viewer per EC-23.
		return "code"
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
