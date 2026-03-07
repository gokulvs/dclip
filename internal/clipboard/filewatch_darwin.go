//go:build darwin

package clipboard

// Use the modern NSPasteboard API to detect file copies from Finder.
// NSPasteboard.changeCount lets us poll efficiently without spawning processes.
// All CGO calls are wrapped with pasteMu (pastelock_darwin.go) to prevent
// concurrent access, and with @try/@catch to survive internal Apple exceptions.

// #cgo CFLAGS: -x objective-c
// #cgo LDFLAGS: -framework AppKit
// #include <stdlib.h>
// #import <AppKit/AppKit.h>
//
// static long pbChangeCount() {
//     @try {
//         return [NSPasteboard generalPasteboard].changeCount;
//     } @catch (NSException *e) {
//         return -2;
//     }
// }
//
// // Returns heap-allocated array of strdup'd UTF-8 paths for files in the
// // clipboard. Caller must free with freeFilePaths().
// static char** pbFilePaths(int *count) {
//     @try {
//         NSPasteboard *pb = [NSPasteboard generalPasteboard];
//         NSDictionary *opts = @{ NSPasteboardURLReadingFileURLsOnlyKey: @YES };
//         NSArray *urls = [pb readObjectsForClasses:@[[NSURL class]] options:opts];
//         if (!urls || [urls count] == 0) { *count = 0; return NULL; }
//         *count = (int)[urls count];
//         char **result = (char **)malloc(*count * sizeof(char *));
//         for (int i = 0; i < *count; i++) {
//             result[i] = strdup([[(NSURL *)urls[i] path] UTF8String]);
//         }
//         return result;
//     } @catch (NSException *e) {
//         *count = 0;
//         return NULL;
//     }
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
				// Read changeCount under lock; release before Go work.
				pasteMu.Lock()
				cur := C.pbChangeCount()
				pasteMu.Unlock()

				if cur == lastCount {
					continue
				}
				lastCount = cur
				paths := darwinFilePaths() // acquires pasteMu internally
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

// darwinFilePaths reads file URLs from NSPasteboard under pasteMu and returns
// only paths that exist on disk. Callers must NOT hold pasteMu.
func darwinFilePaths() []string {
	pasteMu.Lock()
	var n C.int
	raw := C.pbFilePaths(&n)
	count := int(n)
	// Copy C strings into Go memory before releasing the lock.
	var cstrs []string
	if raw != nil && count > 0 {
		cArr := (*[1 << 20]*C.char)(unsafe.Pointer(raw))[:count:count]
		cstrs = make([]string, count)
		for i, cp := range cArr {
			cstrs[i] = C.GoString(cp)
		}
		C.freeFilePaths(raw, n)
	}
	pasteMu.Unlock()

	// os.Stat does not touch NSPasteboard — safe outside the lock.
	paths := make([]string, 0, len(cstrs))
	for _, p := range cstrs {
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}
