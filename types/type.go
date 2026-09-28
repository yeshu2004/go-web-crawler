package types

import (
	// "github.com/nats-io/nats.go"
)

type TupleEvent struct {
	CrawlerID string
	EventID   string
	Word      string
	Count     int
	URLHash   string
}
