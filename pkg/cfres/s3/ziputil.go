package s3

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
)

func extractZipMember(data []byte, member string, maxDecompressed int64) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("source is not a valid zip archive")
	}
	var match *zip.File
	for _, f := range zr.File {
		if f.Name != member {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("ambiguous: multiple entries named %q", member)
		}
		if f.FileInfo().IsDir() {
			return nil, fmt.Errorf("%q is a directory, not a file", member)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%q is a symlink", member)
		}
		match = f
	}
	if match == nil {
		return nil, fmt.Errorf("zip member %q not found", member)
	}
	rc, err := match.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	out, err := io.ReadAll(io.LimitReader(rc, maxDecompressed+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxDecompressed {
		return nil, fmt.Errorf("zip member %q exceeds decompressed size cap", member)
	}
	return out, nil
}
