//go:build darwin

package clipboard

// Use the modern NSPasteboard API to detect file copies from Finder.
// NSPasteboard.changeCount lets us poll efficiently without spawning processes.

// #cgo CFLAGS: -x objective-c
// #cgo LDFLAGS: -framework AppKit
// #include <stdlib.h>
// #import <AppKit/AppKit.h>
//
// static long pbChangeCount() {
//     return [NSPasteboard generalPasteboard].changeCount;
// }
//
// // Returns heap-allocated array of strdup'd UTF-8 paths for files in the
// // clipboard. Caller must free with freeFilePaths().
// static char** pbFilePaths(int *count) {
//     NSPasteboard *pb = [NSPasteboard generalPasteboard];
//     NSDictionary *opts = @{ NSPasteboardURLReadingFileURLsOnlyKey: @YES };
//     NSArray *urls = [pb readObjectsForClasses:@[[NSURL class]] options:opts];
//     if (!urls || [urls count] == 0) { *count = 0; return NULL; }
//     *count = (int)[urls count];
//     char **result = (char **)malloc(*count * sizeof(char *));
//     for (int i = 0; i < *count; i++) {
//         result[i] = strdup([[(NSURL *)urls[i] path] UTF8String]);
//     }
//     return result;
// }
//
// static void freeFilePaths(char **paths, int count) {
//     for (int i = 0; i < count; i++) free(paths[i]);
//     free(paths);
// }
import "C"

import (
	"context"
	"os"
	"time"
	"unsafe"
)

// WatchFiles returns a channel that fires whenever the user copies one or more
// files to the clipboard (e.g. via Cmd+C in Finder).
// The channel is closed when ctx is cancelled.
func WatchFiles(ctx context.Context) <-chan []string {
	ch := make(chan []string, 4)
	go func() {
		defer close(ch)
		var lastCount C.long = -1
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := C.pbChangeCount()
				if cur == lastCount {
					continue
				}
				lastCount = cur
				paths := darwinFilePaths()
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

func darwinFilePaths() []string {
	var n C.int
	raw := C.pbFilePaths(&n)
	if raw == nil || n == 0 {
		return nil
	}
	defer C.freeFilePaths(raw, n)

	cnt := int(n)
	cArr := (*[1 << 20]*C.char)(unsafe.Pointer(raw))[:cnt:cnt]
	paths := make([]string, 0, cnt)
	for _, cp := range cArr {
		p := C.GoString(cp)
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}
