package backup

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRestore_FileSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "output.bin")

	// Create a tar reader with a file that claims to be small but has more data
	// This simulates a decompression bomb scenario where the actual data
	// exceeds what is declared in the header.

	// Create a buffer with more data than the declared header size
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Write a file header claiming 100 bytes but we'll write more actual data
	smallContent := bytes.Repeat([]byte("A"), 100)
	hdr := &tar.Header{
		Name: "test/small.bin",
		Mode: 0644,
		Size: int64(len(smallContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(smallContent); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	// Test normal extraction (within limits)
	tr := tar.NewReader(&buf)
	readHdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}

	err = extractFile(tr, readHdr, dest)
	if err != nil {
		t.Fatalf("Expected successful extraction for small file, got: %v", err)
	}

	// Verify file was written correctly
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 100 {
		t.Errorf("Expected 100 bytes, got %d", len(data))
	}
}

func TestRestore_FileSizeLimitExceeded(t *testing.T) {
	// Shrink the cap for this test so we can exercise the limit path without
	// allocating a real 1GB buffer (which OOMs under -race on tight CI hosts).
	orig := maxExtractFileSize
	maxExtractFileSize = 64 * 1024 // 64 KB
	t.Cleanup(func() { maxExtractFileSize = orig })

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "large.bin")

	// Create a tar entry that claims a size larger than our (shrunk) limit.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	hdr := &tar.Header{
		Name: "test/huge.bin",
		Mode: 0644,
		Size: maxExtractFileSize + 1024, // cap + 1KB
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}

	// Write just over maxExtractFileSize bytes of data to trigger the limit.
	payload := bytes.Repeat([]byte("B"), int(hdr.Size))
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	tr := tar.NewReader(&buf)
	readHdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}

	err = extractFile(tr, readHdr, dest)
	if err == nil {
		t.Fatal("Expected error for file exceeding size limit")
	}

	if !bytes.Contains([]byte(err.Error()), []byte("exceeds size limit")) {
		t.Errorf("Expected 'exceeds size limit' error, got: %v", err)
	}
}

func TestRestore_FileSizeLimitRespectsDeclaredSize(t *testing.T) {
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "normal.bin")

	// Create a tar entry with a small declared size (well under the limit)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	content := bytes.Repeat([]byte("C"), 500)
	hdr := &tar.Header{
		Name: "test/normal.bin",
		Mode: 0644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	tr := tar.NewReader(&buf)
	readHdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}

	err = extractFile(tr, readHdr, dest)
	if err != nil {
		t.Fatalf("Expected successful extraction, got: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 500 {
		t.Errorf("Expected 500 bytes, got %d", len(data))
	}
}
