package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFetchSuccess(t *testing.T) {
	data := makeTarGz(t, map[string]string{"rgd.yaml": "kind: RGD\n"})
	digest := "sha256:" + sha256Hex(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := &Fetcher{HTTPClient: srv.Client()}
	if err := f.Fetch(context.Background(), ArtifactInfo{URL: srv.URL, Digest: digest}, dir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "rgd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "kind: RGD\n" {
		t.Fatalf("contents: %q", string(got))
	}
}

func TestFetchDigestMismatch(t *testing.T) {
	data := makeTarGz(t, map[string]string{"x.txt": "x"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	f := &Fetcher{HTTPClient: srv.Client()}
	err := f.Fetch(context.Background(), ArtifactInfo{URL: srv.URL, Digest: "sha256:0000"}, dir)
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("expected digest error, got %v", err)
	}
}

func TestFetchPathTraversal(t *testing.T) {
	data := makeTarGz(t, map[string]string{"../escape": "no"})
	digest := "sha256:" + sha256Hex(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	f := &Fetcher{HTTPClient: srv.Client()}
	err := f.Fetch(context.Background(), ArtifactInfo{URL: srv.URL, Digest: digest}, dir)
	if err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("expected path traversal error, got %v", err)
	}
}
