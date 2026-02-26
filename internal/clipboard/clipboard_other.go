//go:build !darwin

package clipboard

import (
	"context"

	xclip "golang.design/x/clipboard"
)

// Init initialises the system clipboard (required by golang.design/x/clipboard).
func Init() error {
	return xclip.Init()
}

// WatchText returns a channel that emits text clipboard data on change.
func WatchText(ctx context.Context) <-chan []byte {
	return xclip.Watch(ctx, xclip.FmtText)
}

// WatchImage returns a channel that emits image clipboard data on change.
func WatchImage(ctx context.Context) <-chan []byte {
	return xclip.Watch(ctx, xclip.FmtImage)
}

// WriteText writes text to the system clipboard.
func WriteText(data []byte) {
	xclip.Write(xclip.FmtText, data)
}

// WriteImage writes image data to the system clipboard.
func WriteImage(data []byte) {
	xclip.Write(xclip.FmtImage, data)
}
