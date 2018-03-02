package discard

import "github.com/gokit/history"

var (
	// Discard receives b pointers without performing any action with it.
	Discard = history.HandlerFunc(func(b history.BugLog) error {
		return nil
	})
)
