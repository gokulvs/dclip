// Package clipboard provides helpers for handling clipboard content types
// that the system clipboard library does not natively support (e.g. files).
package clipboard

import (
	"fmt"
	"os"
	"path/filepath"
)

// DownloadDir is the directory where received files are stored.
func DownloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "dclip-downloads")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return dir, nil
}

// WriteFile saves file bytes to ~/dclip-downloads/<filename> and returns the
// path. If filename is empty, it falls back to "clipboard-file".
func WriteFile(filename string, data []byte) (string, error) {
	if filename == "" {
		filename = "clipboard-file"
	}
	dir, err := DownloadDir()
	if err != nil {
		return "", fmt.Errorf("download dir: %w", err)
	}
	dst := filepath.Join(dir, filepath.Base(filename))
	if err := os.WriteFile(dst, data, 0o640); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return dst, nil
}
