//go:build windows

package clipboard

// Use PowerShell + System.Windows.Forms to read CF_HDROP (file drop list)
// from the clipboard. No CGO needed — PowerShell is available on all modern
// Windows versions.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// WatchFiles returns a channel that fires whenever the user copies one or more
// files to the clipboard (e.g. via Ctrl+C in File Explorer).
func WatchFiles(ctx context.Context) <-chan []string {
	ch := make(chan []string, 4)
	go func() {
		defer close(ch)
		var last string
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				raw := winClipboardFiles()
				if raw == last {
					continue
				}
				last = raw
				if raw == "" {
					continue
				}
				paths := parseWinPaths(raw)
				if len(paths) == 0 {
					continue
				}
				select {
				case ch <- paths:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// winClipboardFiles returns newline-separated absolute paths of files in the
// clipboard, or "" if the clipboard contains no files.
func winClipboardFiles() string {
	// System.Windows.Forms.Clipboard.GetFileDropList() is the most reliable way
	// to read CF_HDROP without CGO.
	out, err := exec.Command(
		"powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Add-Type -AssemblyName System.Windows.Forms; `+
			`$f = [System.Windows.Forms.Clipboard]::GetFileDropList(); `+
			`if ($f -and $f.Count -gt 0) { $f -join [char]10 }`,
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func parseWinPaths(raw string) []string {
	var paths []string
	for _, p := range strings.Split(raw, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}
