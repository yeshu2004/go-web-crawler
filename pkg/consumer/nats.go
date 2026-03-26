package consumer

import (
	"context"
	"encoding/json"

	"github/yeshu2004/go-epics/pkg/models"

	"github.com/nats-io/nats.go"
)

type NATSConsumer struct {
	nc      *nats.Conn
	subject string
	group   string
}

func NewNATSConsumer(url, subject, group string) (*NATSConsumer, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return &NATSConsumer{nc: nc, subject: subject, group: group}, nil
}

func (c *NATSConsumer) Start(ctx context.Context, handler Handler) error {
	msgCh := make(chan *nats.Msg, 4096)
	sub, err := c.nc.ChanQueueSubscribe(c.subject, c.group, msgCh)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-msgCh:
			if msg == nil {
				continue
			}
			var event models.PostingEvent
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				continue
			}
			if err := handler(ctx, &event); err != nil {
				continue
			}
		}
	}
}

func (c *NATSConsumer) Close() error {
	c.nc.Close()
	return nil
}
