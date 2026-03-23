package consumer

import (
	"context"
	"encoding/json"

	"github/yeshu2004/go-epics/pkg/models"

	"github.com/segmentio/kafka-go"
)

type KafkaConsumer struct {
	reader *kafka.Reader
}

func NewKafkaConsumer(brokers []string, topic, group string) *KafkaConsumer {
	if len(brokers) == 0 {
		brokers = []string{"localhost:9092"}
	}
	return &KafkaConsumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:  brokers,
			Topic:    topic,
			GroupID:  group,
			MinBytes: 10e3,
			MaxBytes: 10e6,
		}),
	}
}

func (c *KafkaConsumer) Start(ctx context.Context, handler Handler) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			msg, err := c.reader.ReadMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				continue
			}
			var event models.PostingEvent
			if err := json.Unmarshal(msg.Value, &event); err != nil {
				continue
			}
			handler(ctx, &event)
		}
	}
}

func (c *KafkaConsumer) Close() error {
	return c.reader.Close()
}
