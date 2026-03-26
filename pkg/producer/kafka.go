package producer

import (
	"context"
	"encoding/json"

	"github/yeshu2004/go-epics/pkg/models"

	"github.com/segmentio/kafka-go"
)

type KafkaProducer struct {
	writer *kafka.Writer
}

func NewKafkaProducer(brokers []string, topic, sourceID string) *KafkaProducer {
	if len(brokers) == 0 {
		brokers = []string{"localhost:9092"}
	}
	return &KafkaProducer{
		writer: &kafka.Writer{
			Addr:     kafka.TCP(brokers...),
			Topic:    topic,
			Balancer: &kafka.LeastBytes{},
		},
	}
}

func (p *KafkaProducer) Publish(ctx context.Context, event *models.PostingEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(event.URLHash),
		Value: payload,
	})
}

func (p *KafkaProducer) Close() error {
	return p.writer.Close()
}
