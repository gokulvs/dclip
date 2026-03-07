//go:build darwin

package clipboard

// Clipboard watch and read/write for macOS using direct NSPasteboard calls.
//
// Two layers of protection against NSPasteboard crashes on modern macOS:
//
//  1. pasteMu (pastelock_darwin.go) — Go-level mutex that ensures only one
//     goroutine enters an NSPasteboard CGO call at a time.  Without this,
//     three concurrent pollers (WatchText, WatchImage, WatchFiles) each run
//     on their own OS thread, causing SIGSEGV from simultaneous C calls.
//
//  2. @try/@catch in every C helper — NSPasteboard can throw NSGenericException
//     ("collection mutated while being enumerated") when a distributed
//     notification arrives mid-call and mutates its internal type cache.
//     The exception is caught; the helper returns NULL/0 and the next 300 ms
//     poll retries automatically.

// #cgo CFLAGS: -x objective-c
// #cgo LDFLAGS: -framework AppKit
// #include <stdlib.h>
// #import <AppKit/AppKit.h>
//
// static long cb_change_count() {
//     @try {
//         return [NSPasteboard generalPasteboard].changeCount;
//     } @catch (NSException *e) {
//         return -2; // distinct from -1 sentinel; won't match lastCount
//     }
// }
//
// // Returns a heap-allocated UTF-8 string when the clipboard contains plain
// // text, otherwise NULL. Caller must free().
// static char* cb_get_string() {
//     @try {
//         NSString *s = [[NSPasteboard generalPasteboard]
//                             stringForType:NSPasteboardTypeString];
//         if (!s) return NULL;
//         return strdup([s UTF8String]);
//     } @catch (NSException *e) {
//         return NULL;
//     }
// }
//
// static void cb_set_string(const char *s) {
//     @try {
//         NSPasteboard *pb = [NSPasteboard generalPasteboard];
//         [pb clearContents];
//         [pb setString:[NSString stringWithUTF8String:s]
//               forType:NSPasteboardTypeString];
//     } @catch (NSException *e) {}
// }
//
// // Returns heap-allocated PNG bytes when the clipboard contains an image,
// // otherwise NULL. Caller must free().
// static void* cb_get_image(int *len) {
//     @try {
//         NSPasteboard *pb = [NSPasteboard generalPasteboard];
//         NSData *data = [pb dataForType:NSPasteboardTypePNG];
//         if (!data) {
//             // Fall back: convert TIFF → PNG (macOS screenshots are TIFF).
//             NSData *tiff = [pb dataForType:NSPasteboardTypeTIFF];
//             if (tiff) {
//                 NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:tiff];
//                 if (rep)
//                     data = [rep representationUsingType:NSBitmapImageFileTypePNG
//                                             properties:@{}];
//             }
//         }
//         if (!data || [data length] == 0) { *len = 0; return NULL; }
//         *len = (int)[data length];
//         void *result = malloc(*len);
//         memcpy(result, [data bytes], *len);
//         return result;
//     } @catch (NSException *e) {
//         *len = 0;
//         return NULL;
//     }
// }
//
// static void cb_set_image(const void *data, int len) {
//     @try {
//         NSData *d = [NSData dataWithBytes:data length:len];
//         NSPasteboard *pb = [NSPasteboard generalPasteboard];
//         [pb clearContents];
//         [pb setData:d forType:NSPasteboardTypePNG];
//     } @catch (NSException *e) {}
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
				// Hold pasteMu for the entire count-check + read cycle so no
				// other goroutine can touch NSPasteboard at the same time.
				pasteMu.Lock()
				cur := C.cb_change_count()
				if cur == lastCount {
					pasteMu.Unlock()
					continue
				}
				lastCount = cur
				raw := C.cb_get_string()
				var data []byte
				if raw != nil {
					data = []byte(C.GoString(raw))
					C.free(unsafe.Pointer(raw))
				}
				pasteMu.Unlock()

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
				pasteMu.Lock()
				cur := C.cb_change_count()
				if cur == lastCount {
					pasteMu.Unlock()
					continue
				}
				lastCount = cur
				var n C.int
				imgData := C.cb_get_image(&n)
				var b []byte
				if imgData != nil && n > 0 {
					b = C.GoBytes(imgData, n)
					C.free(imgData)
				}
				pasteMu.Unlock()

				if len(b) == 0 {
					continue
				}
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
	pasteMu.Lock()
	C.cb_set_string(cs)
	pasteMu.Unlock()
	C.free(unsafe.Pointer(cs))
}

// WriteImage writes PNG image bytes to the macOS clipboard.
func WriteImage(data []byte) {
	if len(data) == 0 {
		return
	}
	pasteMu.Lock()
	C.cb_set_image(unsafe.Pointer(&data[0]), C.int(len(data)))
	pasteMu.Unlock()
}
