package engine

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EnsureRawISO checks if the given file is a ZIP archive containing an ISO.
// If it is a ZIP, it unpacks the nested ISO to destDir and returns that path.
// If it is already a raw ISO, it returns filePath directly without modification.
func EnsureRawISO(filePath, destDir string, onProgress func(written, total int64)) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return filePath, err
	}

	header := make([]byte, 4)
	_, err = file.Read(header)
	file.Close()
	if err != nil {
		return filePath, err
	}

	// Magic header bytes for ZIP files: PK\x03\x04
	isZip := bytes.Equal(header, []byte{0x50, 0x4B, 0x03, 0x04})
	if !isZip && !strings.HasSuffix(strings.ToLower(filePath), ".zip") {
		return filePath, nil
	}

	r, err := zip.OpenReader(filePath)
	if err != nil {
		return filePath, nil
	}
	defer r.Close()

	var targetFile *zip.File
	for _, f := range r.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".iso") {
			targetFile = f
			break
		}
	}

	// Fallback: choose the largest file inside the archive
	if targetFile == nil {
		var maxSz uint64
		for _, f := range r.File {
			if !f.FileInfo().IsDir() && f.UncompressedSize64 > maxSz {
				maxSz = f.UncompressedSize64
				targetFile = f
			}
		}
	}

	if targetFile == nil {
		return filePath, fmt.Errorf("archive does not contain an ISO image")
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}

	outPath := filepath.Join(destDir, filepath.Base(targetFile.Name))
	if !strings.HasSuffix(strings.ToLower(outPath), ".iso") {
		outPath += ".iso"
	}

	// If already extracted and file sizes match, reuse directly
	if fi, err := os.Stat(outPath); err == nil && uint64(fi.Size()) == targetFile.UncompressedSize64 {
		return outPath, nil
	}

	rc, err := targetFile.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return "", err
	}
	defer out.Close()

	total := int64(targetFile.UncompressedSize64)
	buf := make([]byte, 1024*1024*4) // 4MB buffer for speed
	var written int64

	for {
		n, rErr := rc.Read(buf)
		if n > 0 {
			wN, wErr := out.Write(buf[:n])
			written += int64(wN)
			if onProgress != nil {
				onProgress(written, total)
			}
			if wErr != nil {
				return "", wErr
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return "", rErr
		}
	}

	return outPath, nil
}
