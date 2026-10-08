package notify

import (
	"context"

	"github.com/eiffelbeef/jelly-diff/internal/storage"
)

// Notifier is the interface implemented by all notification backends.
type Notifier interface {
	Notify(ctx context.Context, events []storage.Event) error
}

// NoOp is a notifier that does nothing.
type NoOp struct{}

func (NoOp) Notify(_ context.Context, _ []storage.Event) error { return nil }
