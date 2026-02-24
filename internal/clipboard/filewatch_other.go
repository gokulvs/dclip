//go:build !darwin && !windows

package clipboard

import "context"

// WatchFiles is not supported on this platform.
// Returns nil — callers must handle a nil channel (it blocks forever, so wrap
// in a select with ctx.Done()).
func WatchFiles(_ context.Context) <-chan []string { return nil }
