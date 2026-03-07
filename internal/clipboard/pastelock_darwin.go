//go:build darwin

package clipboard

import "sync"

// pasteMu serialises every CGO call that touches NSPasteboard.
//
// NSPasteboard is NOT thread-safe: calling it concurrently from multiple OS
// threads (which Go creates when goroutines block in CGO) causes SIGSEGV on
// modern macOS. This mutex ensures only one goroutine is inside an
// NSPasteboard CGO call at any time.
//
// Rules:
//   - Lock before every CGO call that reads or writes NSPasteboard.
//   - Unlock as soon as the CGO call (and any immediate C string copies) return.
//   - Never hold pasteMu while blocking in Go (channel sends, os.Stat, etc.).
var pasteMu sync.Mutex
