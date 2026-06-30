//go:build unit

package s3

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
)

func zipWith(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zipWith: Create(%q): %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zipWith: Write(%q): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zipWith: Close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractZipMember_Happy(t *testing.T) {
	z := zipWith(t, map[string]string{"carys-cars.jar": "JARBYTES"})
	got, err := extractZipMember(z, "carys-cars.jar", 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "JARBYTES" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractZipMember_Missing(t *testing.T) {
	z := zipWith(t, map[string]string{"other.jar": "x"})
	if _, err := extractZipMember(z, "carys-cars.jar", 1<<30); err == nil {
		t.Fatal("expected missing-member error")
	}
}

func TestExtractZipMember_Duplicate(t *testing.T) {
	// Build a zip manually with two entries of the same name.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for range 2 {
		w, err := zw.Create("dup")
		if err != nil {
			t.Fatalf("Create dup: %v", err)
		}
		if _, err := w.Write([]byte("content")); err != nil {
			t.Fatalf("Write dup: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	z := buf.Bytes()
	if _, err := extractZipMember(z, "dup", 1<<30); err == nil {
		t.Fatal("expected ambiguous-member error")
	}
}

func TestExtractZipMember_RejectsDirAndSymlink(t *testing.T) {
	// Directory entry: name ends with "/"
	var dirBuf bytes.Buffer
	zw := zip.NewWriter(&dirBuf)
	if _, err := zw.Create("adir/"); err != nil {
		t.Fatalf("Create dir: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := extractZipMember(dirBuf.Bytes(), "adir/", 1<<30); err == nil {
		t.Fatal("expected directory-entry error")
	}

	// Symlink entry: set mode to os.ModeSymlink
	var symBuf bytes.Buffer
	zw2 := zip.NewWriter(&symBuf)
	hdr := &zip.FileHeader{Name: "link"}
	hdr.SetMode(0777 | os.ModeSymlink)
	w, err := zw2.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("CreateHeader symlink: %v", err)
	}
	if _, err := w.Write([]byte("target")); err != nil {
		t.Fatalf("Write symlink: %v", err)
	}
	if err := zw2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := extractZipMember(symBuf.Bytes(), "link", 1<<30); err == nil {
		t.Fatal("expected symlink-entry error")
	}
}

func TestExtractZipMember_ZipBombCap(t *testing.T) {
	z := zipWith(t, map[string]string{"big": strings.Repeat("A", 1<<20)})
	if _, err := extractZipMember(z, "big", 1<<10); err == nil {
		t.Fatal("expected decompressed-size cap error")
	}
}
