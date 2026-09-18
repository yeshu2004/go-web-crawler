package types

import (
	"context"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"time"
	// "github.com/nats-io/nats.go"
)

type TupleEvent struct {
	Id      string
	Word    string
	Count   int
	URLHash string
}

// Ack implements [jetstream.Msg].
func (t TupleEvent) Ack() error {
	panic("unimplemented")
}

// Data implements [jetstream.Msg].
func (t TupleEvent) Data() []byte {
	panic("unimplemented")
}

// DoubleAck implements [jetstream.Msg].
func (t TupleEvent) DoubleAck(context.Context) error {
	panic("unimplemented")
}

// Headers implements [jetstream.Msg].
func (t TupleEvent) Headers() nats.Header {
	panic("unimplemented")
}

// InProgress implements [jetstream.Msg].
func (t TupleEvent) InProgress() error {
	panic("unimplemented")
}

// Metadata implements [jetstream.Msg].
func (t TupleEvent) Metadata() (*jetstream.MsgMetadata, error) {
	panic("unimplemented")
}

// Nak implements [jetstream.Msg].
func (t TupleEvent) Nak() error {
	panic("unimplemented")
}

// NakWithDelay implements [jetstream.Msg].
func (t TupleEvent) NakWithDelay(delay time.Duration) error {
	panic("unimplemented")
}

// Reply implements [jetstream.Msg].
func (t TupleEvent) Reply() string {
	panic("unimplemented")
}

// Subject implements [jetstream.Msg].
func (t TupleEvent) Subject() string {
	panic("unimplemented")
}

// Term implements [jetstream.Msg].
func (t TupleEvent) Term() error {
	panic("unimplemented")
}

// TermWithReason implements [jetstream.Msg].
func (t TupleEvent) TermWithReason(reason string) error {
	panic("unimplemented")
}
