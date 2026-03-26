package producer

import (
	"context"

	"github/yeshu2004/go-epics/pkg/models"
)

// Producer publishes posting events to an external broker.
type Producer interface {
	Publish(ctx context.Context, event *models.PostingEvent) error
	Close() error
}
