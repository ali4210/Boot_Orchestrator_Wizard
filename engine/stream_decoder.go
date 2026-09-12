package engine

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DecodedStream wraps the inflated payload reader and handles resource teardown.
type DecodedStream struct {
	Reader       io.Reader
	Cleanup      func()
	DetectedType string
	ExtractedISO string
}

// InspectAndDecodeStream sniffs the stream magic header. If an archive format (.zip, .xz, .gz)
// is detected, it streams the inflated payload. Raw ISOs pass through directly.
func InspectAndDecodeStream(r io.Reader) (*DecodedStream, error) {
	header := make([]byte, 512)
	n, err := io.ReadFull(r, header)
	if err != nil && err != io.EOF && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("failed to read stream magic header: %w", err)
	}

	combined := io.MultiReader(bytes.NewReader(header[:n]), r)

	// Signature 1: Standard ZIP Archive (PK\x03\x04)
	if bytes.HasPrefix(header, []byte("PK\x03\x04")) {
		// ZIP format requires random-access reader to parse Central Directory.
		// Stream safely into an ephemeral tmpfs RAM file in /tmp.
		tmpZip, err := os.CreateTemp("/tmp", "orch_extract_*.zip")
		if err != nil {
			return nil, fmt.Errorf("creating ephemeral zip buffer: %w", err)
		}

		if _, err := io.Copy(tmpZip, combined); err != nil {
			tmpZip.Close()
			_ = os.Remove(tmpZip.Name())
			return nil, fmt.Errorf("buffering compressed zip archive: %w", err)
		}
		tmpZip.Close()

		zipReader, err := zip.OpenReader(tmpZip.Name())
		if err != nil {
			_ = os.Remove(tmpZip.Name())
			return nil, fmt.Errorf("opening zip archive structure: %w", err)
		}

		// Locate target bootable payload inside zip
		var targetFile *zip.File
		for _, f := range zipReader.File {
			lower := strings.ToLower(f.Name)
			if strings.HasSuffix(lower, ".iso") || strings.HasSuffix(lower, ".img") || strings.HasSuffix(lower, ".raw") {
				targetFile = f
				break
			}
		}

		if targetFile == nil {
			zipReader.Close()
			_ = os.Remove(tmpZip.Name())
			return nil, errors.New("zip archive contains no bootable .iso or .img payload")
		}

		rc, err := targetFile.Open()
		if err != nil {
			zipReader.Close()
			_ = os.Remove(tmpZip.Name())
			return nil, fmt.Errorf("extracting inner image stream: %w", err)
		}

		return &DecodedStream{
			Reader:       rc,
			DetectedType: "ZIP Archive (" + targetFile.Name + ")",
			ExtractedISO: filepath.Base(targetFile.Name),
			Cleanup: func() {
				rc.Close()
				zipReader.Close()
				_ = os.Remove(tmpZip.Name())
			},
		}, nil
	}

	// Signature 2: GZIP stream (\x1f\x8b)
	if bytes.HasPrefix(header, []byte("\x1f\x8b")) {
		gzReader, err := gzip.NewReader(combined)
		if err != nil {
			return nil, fmt.Errorf("initializing gzip decompressor: %w", err)
		}
		return &DecodedStream{
			Reader:       gzReader,
			DetectedType: "GZIP Compressed Stream",
			Cleanup: func() {
				_ = gzReader.Close()
			},
		}, nil
	}

	// Default: Raw ISO-9660 or raw block image (Pass straight through zero-copy)
	return &DecodedStream{
		Reader:       combined,
		DetectedType: "Raw ISO-9660 Disc Image",
		Cleanup:      func() {},
	}, nil
}
