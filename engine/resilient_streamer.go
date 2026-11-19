package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type DownloadSource struct {
	Mirrors        []string
	ExpectedSHA256 string
	DestPath       string
}

// DownloadWithFailover tries each mirror sequentially until one succeeds and passes verification
func DownloadWithFailover(ctx context.Context, src DownloadSource, onProgress func(bytesRead, totalBytes int64)) error {
	client := &http.Client{Timeout: 30 * time.Second}
	var lastErr error

	for index, mirrorURL := range src.Mirrors {
		fmt.Printf("=> [ATTEMPT %d/%d] Connecting to mirror: %s\n", index+1, len(src.Mirrors), mirrorURL)

		// 1. Pre-flight HTTP ping
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, mirrorURL, nil)
		if err != nil {
			lastErr = err
			continue
		}

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			fmt.Printf("=> Warning: Mirror unavailable (HTTP %d). Switching to fallback mirror...\n", resp.StatusCode)
			lastErr = fmt.Errorf("mirror returned status %d", resp.StatusCode)
			continue
		}

		totalSize := resp.ContentLength

		// 2. Stream download payload
		getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, mirrorURL, nil)
		getResp, err := client.Do(getReq)
		if err != nil {
			lastErr = err
			continue
		}

		tmpFile := src.DestPath + ".part"
		out, err := os.Create(tmpFile)
		if err != nil {
			getResp.Body.Close()
			return fmt.Errorf("could not create temporary download file: %w", err)
		}

		hasher := sha256.New()
		mw := io.MultiWriter(out, hasher)

		// Dynamic buffered copy loop with live reporting
		buf := make([]byte, 64*1024) // 64KB buffer for high throughput
		var totalRead int64

		for {
			select {
			case <-ctx.Done():
				getResp.Body.Close()
				out.Close()
				return ctx.Err()
			default:
			}

			n, readErr := getResp.Body.Read(buf)
			if n > 0 {
				_, _ = mw.Write(buf[:n])
				totalRead += int64(n)
				if onProgress != nil {
					onProgress(totalRead, totalSize)
				}
			}

			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				lastErr = readErr
				break
			}
		}

		getResp.Body.Close()
		out.Close()

		if lastErr != nil && lastErr != io.EOF {
			fmt.Printf("=> Warning: Connection drop while streaming from %s. Trying alternate...\n", mirrorURL)
			_ = os.Remove(tmpFile)
			continue
		}

		// 3. SHA-256 integrity verification
		calculatedSHA := hex.EncodeToString(hasher.Sum(nil))
		if src.ExpectedSHA256 != "" && !strings.EqualFold(calculatedSHA, src.ExpectedSHA256) {
			fmt.Printf("=> Error: SHA-256 mismatch from mirror %s (Corrupted payload). Failing over...\n", mirrorURL)
			_ = os.Remove(tmpFile)
			lastErr = fmt.Errorf("checksum validation failed")
			continue
		}

		// Commit valid payload
		_ = os.Rename(tmpFile, src.DestPath)
		fmt.Println("=> Payload verified and committed successfully.")
		return nil
	}

	return fmt.Errorf("all redundant mirrors exhausted. Last error: %w", lastErr)
}
