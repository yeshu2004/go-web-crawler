package producer

import (
	"context"
	"encoding/json"

	"github/yeshu2004/go-epics/pkg/models"

	"github.com/nats-io/nats.go"
)

type NATSProducer struct {
	nc      *nats.Conn
	subject string
}

func NewNATSProducer(url, subject string) (*NATSProducer, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return &NATSProducer{nc: nc, subject: subject}, nil
}

func (p *NATSProducer) Publish(ctx context.Context, event *models.PostingEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := p.nc.Publish(p.subject, payload); err != nil {
		return err
	}
	return p.nc.FlushWithContext(ctx)
}

func (p *NATSProducer) Close() error {
	p.nc.Close()
	return nil
}
