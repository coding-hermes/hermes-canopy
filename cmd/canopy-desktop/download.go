package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// downloadArtifact streams the artifact bytes to dst, calling onProgress
// periodically with (written, total). Total comes from Content-Length when
// the server sends it. Used by the releaseFeedProvider's Download; the
// Wails updater wraps this with digest/signature verification while
// streaming, so no bytes reach disk unverified.
func downloadArtifact(ctx context.Context, client *http.Client, url string, dst io.Writer, onProgress func(written, total int64)) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("canopy-desktop: create download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("canopy-desktop: download artifact: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("canopy-desktop: download returned HTTP %s", resp.Status)
	}
	total := resp.ContentLength
	buf := make([]byte, 64*1024)
	var written int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("canopy-desktop: stage artifact: %w", writeErr)
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("canopy-desktop: stream artifact: %w", readErr)
		}
	}
}
