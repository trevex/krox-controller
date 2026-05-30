package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MaxArtifactBytes caps the in-memory artifact payload to defend against
// unbounded downloads. Tunable when we hit real-world larger artifacts.
const MaxArtifactBytes = 100 << 20 // 100 MiB

// Fetcher downloads Flux artifact tarballs, verifies digests, and untars
// into a target directory.
type Fetcher struct {
	HTTPClient *http.Client
}

// Fetch downloads info.URL, verifies it matches info.Digest ("sha256:<hex>"),
// then untars into destDir. Rejects entries whose paths would escape destDir.
func (f *Fetcher) Fetch(ctx context.Context, info ArtifactInfo, destDir string) error {
	client := f.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", info.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fetch %s: status %d", info.URL, resp.StatusCode)
	}

	// Buffer the entire body so we can verify digest before untaring.
	// (Sound semantics: a wrong digest must not produce on-disk files.)
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxArtifactBytes+1))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > MaxArtifactBytes {
		return fmt.Errorf("artifact exceeds maximum size %d bytes", MaxArtifactBytes)
	}
	sum := sha256.Sum256(body)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, info.Digest) {
		return fmt.Errorf("digest mismatch: got %s want %s", got, info.Digest)
	}

	gzr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gzr.Close()
	return untar(tar.NewReader(gzr), destDir)
}

func untar(tr *tar.Reader, destDir string) error {
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		target := filepath.Join(absDest, clean)
		if !strings.HasPrefix(target+string(os.PathSeparator), absDest+string(os.PathSeparator)) {
			return fmt.Errorf("path traversal blocked: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// skip symlinks/devices etc.
		}
	}
}
