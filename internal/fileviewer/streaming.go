// Range-aware file streaming (spec §14 GET /files/{id}/stream).
//
// parseRangeHeader implements a strict single-range parser:
//   - "bytes=0-1023"  → start=0, end=1023
//   - "bytes=500-"    → start=500, end=size-1
//   - "bytes=-500"    → suffix range: the last 500 bytes
//
// A multi-range header ("bytes=0-1,5-6") is rejected as invalid rather
// than partially honored — the viewers issue single ranges only.
// Malformed headers map to ErrInvalidRangeHeader (400); ranges that cannot
// be satisfied against the actual file size map to
// ErrRangeNotSatisfiable (416).

package fileviewer

import (
	"fmt"
	"strconv"
	"strings"
)

// byteRange is a resolved, inclusive byte range.
type byteRange struct {
	start int64
	end   int64 // inclusive
}

func (br byteRange) length() int64 { return br.end - br.start + 1 }

// parseRangeHeader parses a Range header against a file of the given size.
// Returns (nil, nil) when the header is absent/empty (plain 200 full-body
// response).
func parseRangeHeader(header string, size int64) (*byteRange, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return nil, ErrInvalidRangeHeader
	}
	if strings.Contains(spec, ",") {
		return nil, ErrInvalidRangeHeader // multi-range unsupported
	}
	spec = strings.TrimSpace(spec)
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return nil, ErrInvalidRangeHeader
	}
	startStr, endStr := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])

	var br byteRange
	switch {
	case startStr == "" && endStr == "":
		return nil, ErrInvalidRangeHeader
	case startStr == "":
		// Suffix form: bytes=-N → last N bytes.
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return nil, ErrInvalidRangeHeader
		}
		if size <= 0 || n > size {
			return nil, ErrRangeNotSatisfiable
		}
		br = byteRange{start: size - n, end: size - 1}
	case endStr == "":
		// Open-ended form: bytes=N- → N..size-1.
		n, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil || n < 0 {
			return nil, ErrInvalidRangeHeader
		}
		if n >= size {
			return nil, ErrRangeNotSatisfiable
		}
		br = byteRange{start: n, end: size - 1}
	default:
		start, err1 := strconv.ParseInt(startStr, 10, 64)
		end, err2 := strconv.ParseInt(endStr, 10, 64)
		if err1 != nil || err2 != nil || start < 0 || end < start {
			return nil, ErrInvalidRangeHeader
		}
		if start >= size || end >= size {
			// Spec §12.1 test 15: a range exceeding the file size is 416
			// RANGE_NOT_SATISFIABLE (deliberately stricter than RFC 9110's
			// end-clamping, which the server-side viewers never need).
			return nil, ErrRangeNotSatisfiable
		}
		br = byteRange{start: start, end: end}
	}
	if br.start < 0 || br.end < br.start || br.start >= size {
		return nil, fmt.Errorf("fileviewer: range %q not satisfiable for size %d", header, size)
	}
	return &br, nil
}
