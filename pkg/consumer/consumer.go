package consumer

import (
	"context"

	"github/yeshu2004/go-epics/pkg/models"
)

// Handler processes one consumed posting event.
type Handler func(ctx context.Context, event *models.PostingEvent) error

// Consumer reads events from the broker and passes them to a handler.
type Consumer interface {
	Start(ctx context.Context, handler Handler) error
	Close() error
}
