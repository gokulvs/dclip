//go:build darwin

package clipboard

// Clipboard watch and read/write for macOS using direct NSPasteboard calls.
//
// golang.design/x/clipboard v0.7.1 crashes on modern macOS (SIGTRAP inside
// clipboard_read_string) because its CGO layer calls AppKit APIs that trigger
// a thread-safety assertion when invoked from a Go goroutine.
//
// Our approach — polling changeCount + reading stringForType/dataForType from
// a background goroutine — is safe: NSPasteboard read operations are
// thread-safe on macOS 10.15+ and we avoid the complex Cocoa machinery the
// external library uses.

// #cgo CFLAGS: -x objective-c
// #cgo LDFLAGS: -framework AppKit
// #include <stdlib.h>
// #import <AppKit/AppKit.h>
//
// static long cb_change_count() {
//     return [NSPasteboard generalPasteboard].changeCount;
// }
//
// // Returns a heap-allocated UTF-8 string when the clipboard contains plain
// // text, otherwise NULL. Caller must free().
// static char* cb_get_string() {
//     NSString *s = [[NSPasteboard generalPasteboard]
//                         stringForType:NSPasteboardTypeString];
//     if (!s) return NULL;
//     return strdup([s UTF8String]);
// }
//
// static void cb_set_string(const char *s) {
//     NSPasteboard *pb = [NSPasteboard generalPasteboard];
//     [pb clearContents];
//     [pb setString:[NSString stringWithUTF8String:s]
//           forType:NSPasteboardTypeString];
// }
//
// // Returns heap-allocated PNG bytes when the clipboard contains an image,
// // otherwise NULL. Caller must free().
// static void* cb_get_image(int *len) {
//     NSPasteboard *pb = [NSPasteboard generalPasteboard];
//     NSData *data = [pb dataForType:NSPasteboardTypePNG];
//     if (!data) {
//         // Fall back: convert TIFF → PNG (macOS screenshots are TIFF).
//         NSData *tiff = [pb dataForType:NSPasteboardTypeTIFF];
//         if (tiff) {
//             NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:tiff];
//             if (rep)
//                 data = [rep representationUsingType:NSBitmapImageFileTypePNG
//                                         properties:@{}];
//         }
//     }
//     if (!data || [data length] == 0) { *len = 0; return NULL; }
//     *len = (int)[data length];
//     void *result = malloc(*len);
//     memcpy(result, [data bytes], *len);
//     return result;
// }
//
// static void cb_set_image(const void *data, int len) {
//     NSData *d = [NSData dataWithBytes:data length:len];
//     NSPasteboard *pb = [NSPasteboard generalPasteboard];
//     [pb clearContents];
//     [pb setData:d forType:NSPasteboardTypePNG];
// }
import "C"

import (
	"context"
	"time"
	"unsafe"
)

const cbPollInterval = 300 * time.Millisecond

// Init is a no-op on macOS — NSPasteboard needs no explicit initialisation.
func Init() error { return nil }

// WatchText returns a channel that emits the clipboard text whenever the
// pasteboard changes to contain plain text. Closed when ctx is cancelled.
func WatchText(ctx context.Context) <-chan []byte {
	ch := make(chan []byte, 4)
	go func() {
		defer close(ch)
		var lastCount C.long = -1
		t := time.NewTicker(cbPollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := C.cb_change_count()
				if cur == lastCount {
					continue
				}
				lastCount = cur
				raw := C.cb_get_string()
				if raw == nil {
					continue
				}
				data := []byte(C.GoString(raw))
				C.free(unsafe.Pointer(raw))
				if len(data) == 0 {
					continue
				}
				select {
				case ch <- data:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// WatchImage returns a channel that emits PNG clipboard data whenever the
// pasteboard changes to contain an image. Closed when ctx is cancelled.
func WatchImage(ctx context.Context) <-chan []byte {
	ch := make(chan []byte, 4)
	go func() {
		defer close(ch)
		var lastCount C.long = -1
		t := time.NewTicker(cbPollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := C.cb_change_count()
				if cur == lastCount {
					continue
				}
				lastCount = cur
				var n C.int
				data := C.cb_get_image(&n)
				if data == nil || n == 0 {
					continue
				}
				b := C.GoBytes(data, n)
				C.free(data)
				select {
				case ch <- b:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// WriteText writes plain text to the macOS clipboard.
func WriteText(data []byte) {
	cs := C.CString(string(data))
	defer C.free(unsafe.Pointer(cs))
	C.cb_set_string(cs)
}

// WriteImage writes PNG image bytes to the macOS clipboard.
func WriteImage(data []byte) {
	if len(data) == 0 {
		return
	}
	C.cb_set_image(unsafe.Pointer(&data[0]), C.int(len(data)))
}
