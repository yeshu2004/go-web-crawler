package nats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github/yeshu2004/go-epics/db"
	"github/yeshu2004/go-epics/types"

	natsclient "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName     = "TUPLE"
	streamSubjects = "TUPLE.*"
	batchSize      = 1000
	maxBatchEvents = 5000
	flushMaxRetry  = 3
	flushInterval  = 5 * time.Second
	maxDLQRetry    = 3
	NumPartitions  = 4
)

type Client struct {
	js jetstream.JetStream
	pg *sql.DB
}

func NewNATSANDPGConn() (*Client, error) {
	conn, err := natsclient.Connect(natsclient.DefaultURL)
	if err != nil {
		return nil, fmt.Errorf("nats connection: %w", err)
	}

	js, err := jetstream.New(conn)
	if err != nil {
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	pg, err := db.ConnectPostgresSQl()
	if err != nil {
		return nil, err
	}
	log.Printf("PGSQL connected....")

	return &Client{js: js, pg: pg}, nil
}

func (c *Client) CreateDLQStream(ctx context.Context) error {
	if _, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        "DLQ",
		Description: "DLQ for failed tupleEvents",
		Subjects:    []string{"DLQ.>"},
		Storage:     jetstream.FileStorage,
		Retention:   jetstream.LimitsPolicy,
		Discard:     jetstream.DiscardOld,
		MaxMsgs:     -1,
		MaxBytes:    -1,
		Replicas:    1,
	}); err != nil {
		return fmt.Errorf("create DLQ stream: %w", err)
	}

	return nil
}

func (c *Client) PublishTupleEventsDLQ(ctx context.Context, payload []byte) error {
	var lastErr error
	for i := 1; i <= maxDLQRetry; i++ {
		_, err := c.js.Publish(ctx, "DLQ.tuple-event", payload)
		if err != nil {
			lastErr = err
			log.Printf("DLQ publish tupleEvent error: %v\n", err)
			if i == maxDLQRetry {
				break
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * time.Second):
			}

			continue
		}

		return nil
	}

	log.Printf("CRITICAL: failed to publish event to DLQ after %d retries: %v", maxDLQRetry, lastErr)
	return fmt.Errorf("DLQ publish: %w", lastErr)
}

func (c *Client) CreateTupleStream(ctx context.Context) error {
	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        streamName,
		Description: "Word tuple processing stream",
		Subjects:    []string{streamSubjects},
		Storage:     jetstream.FileStorage,
		Retention:   jetstream.LimitsPolicy,
		Discard:     jetstream.DiscardOld,
		MaxMsgs:     -1,
		MaxBytes:    -1,
		Replicas:    1,
	})
	return err
}

func (c *Client) PublishTupleEvent(ctx context.Context, partitionID int, payload []byte) error {
	subject := partitionSubject(partitionID)
	if _, err := c.js.Publish(ctx, subject, payload); err != nil {
		return fmt.Errorf("publish to %s: %w", subject, err)
	}
	return nil
}

// StartConsumer creates or reattaches to the durable consumer for partitionID,
// then runs the consume loop. Blocks until ctx is cancelled.
// basically StartConsumer creates the consumer AND runs the loop in one call.
func (c *Client) StartConsumer(ctx context.Context, partitionID int) error {
	name := consumerName(partitionID)

	// creates or re-attaches the consumer
	consumer, err := c.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Name:          name,
		Durable:       name,
		FilterSubject: partitionSubject(partitionID),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		ReplayPolicy:  jetstream.ReplayInstantPolicy,
	})
	if err != nil {
		return fmt.Errorf("create consumer partition %d: %w", partitionID, err)
	}
	log.Printf("[consumer-%d] ready on subject %s", partitionID, partitionSubject(partitionID))

	return c.consume(ctx, consumer, partitionID)
}

// main design is at-least-once NATS delivery with idempotent PostgreSQL processing,
// not literal exactly-once message delivery, but this gives once message delivery kinda...
// because the event ID insert and word-count increment happen in the same transaction, a
// redelivery after a successful DB commit won't increment the word again.
func (c *Client) consume(ctx context.Context, consumer jetstream.Consumer, partitionID int) error {
	var pendingMsgs []jetstream.Msg
	var pendingEvents []types.TupleEvent
	pendingCount := 0
	var lastFlush time.Time

	// starts consuming the tuple events
	for {
		select {
		case <-ctx.Done():
			// flush the remaining tuple
			if len(pendingEvents) > 0 {
				flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

				err := c.flushBatchWithRetry(flushCtx, pendingEvents, pendingMsgs, partitionID)
				cancel()

				// DLQ
				if err != nil {
					log.Printf("consumer-%d] final flush failed i.e trying DLQ: %v", partitionID, err)

					dlqErr := c.pushToDLQ(pendingEvents, partitionID)
					if dlqErr == nil {
						// ACK
						for _, msg := range pendingMsgs {
							if ackErr := msg.Ack(); ackErr != nil {
								log.Printf("[consumer-%d] ACK after DLQ failed: %v", partitionID, ackErr)
							}
						}
						log.Printf("[consumer-%d] ACK %d events in DLQ", partitionID, len(pendingEvents))
					} else {
						return dlqErr
					}

				}
			}
			log.Printf("[consumer-%d] shutting down", partitionID)
			return nil
		default:
			msgs, err := consumer.Fetch(10, jetstream.FetchMaxWait(time.Second))
			if err == nil {
				for msg := range msgs.Messages() {
					// process the tuple event and add it into the map & update count
					events, err := c.processTuple(partitionID, msg.Data())
					if err != nil {
						log.Printf("[consumer-%d] processing error: %v — nacking", partitionID, err)
						msg.Nak()
						continue
					}

					if pendingCount == 0 {
						lastFlush = time.Now()
					}

					for _, event := range events {
						pendingEvents = append(pendingEvents, event)
					}
					pendingMsgs = append(pendingMsgs, msg)
					pendingCount++
				}

			} else if !errors.Is(err, context.Canceled) {
				log.Printf("[consumer-%d] fetch error: %v", partitionID, err)
			}

			// flush if size threshold OR time threshold is reached.
			shouldFlush := pendingCount >= batchSize || len(pendingEvents) >= maxBatchEvents || (pendingCount > 0 && time.Since(lastFlush) >= flushInterval)

			if shouldFlush {
				log.Printf("[consumer-%d] flushing %d events, %d unique words", partitionID, pendingCount, len(pendingEvents))
				err := c.flushBatchWithRetry(ctx, pendingEvents, pendingMsgs, partitionID)
				if err != nil {
					log.Printf("[consumer-%d] flush failed: %v", partitionID, err)

					// TODO: DLQ
					dlqErr := c.pushToDLQ(pendingEvents, partitionID)
					if dlqErr == nil {
						// ACK
						for _, msg := range pendingMsgs {
							if ackErr := msg.Ack(); ackErr != nil {
								log.Printf("[consumer-%d] ACK after DLQ failed: %v", partitionID, ackErr)
							}
						}
						log.Printf("[consumer-%d] ACK %d events in DLQ", partitionID, len(pendingEvents))
					} else {
						return dlqErr
					}
					// select {
					// case <-time.After(2 * time.Second):
					// case <-ctx.Done():
					// }
					// continue // retry same batch

				}

				// log.Printf("[consumer-%v] info: %v, %d, %v, %v", partitionID, pendingEvents, pendingCount, pendingMsgs, lastFlush)

				// tupleBatch = make(map[string]int, batchSize);
				pendingEvents = nil
				pendingMsgs = nil
				pendingCount = 0
				lastFlush = time.Time{}
			}

		}
	}
}

func (c *Client) pushToDLQ(pendingEvents []types.TupleEvent, partitionID int) error {
	payload, err := json.Marshal(pendingEvents)
	if err != nil {
		return fmt.Errorf("[consumer-%d] CRITICAL: could not marshal DLQ payload: %v", partitionID, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if dlqErr := c.PublishTupleEventsDLQ(ctx, payload); dlqErr != nil {
		return fmt.Errorf("[consumer-%d] CRITICAL: DLQ publish failed, %d events lost: %v", partitionID, len(pendingEvents), dlqErr)
	}

	log.Printf("[consumer-%d] pushed %d events in DLQ", partitionID, len(pendingEvents))
	return nil
}

func (c *Client) flushBatchWithRetry(ctx context.Context, events []types.TupleEvent, pending []jetstream.Msg, partitionID int) error {
	if len(events) == 0 {
		return nil
	}

	var lastErr error
	for i := 1; i <= flushMaxRetry; i++ {
		if err := c.flushDB(ctx, events, partitionID); err != nil {
			lastErr = err
			log.Printf("[consumer-%d] flush attempt %d/%d failed: %v", partitionID, i, flushMaxRetry, err)
			if i == flushMaxRetry {
				break
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * time.Second):
			}

			continue
		}

		for _, msg := range pending {
			if err := msg.Ack(); err != nil {
				log.Printf("[consumer-%d] ACK failed: %v", partitionID, err)
			}
		}

		return nil
	}
	return fmt.Errorf("flush failed after %d attempts: %w", flushMaxRetry, lastErr)
}

// this is at-least-once delivery with effectively-once database effects,
// assuming each published event has a stable, unique ID
func (c *Client) flushDB(ctx context.Context, events []types.TupleEvent, partitionID int) error {
	tx, err := c.pg.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	query := `INSERT INTO word_counts(word, count) VALUES ($1, $2) ON CONFLICT (word) DO UPDATE SET count = word_counts.count + EXCLUDED.count`

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	log.Printf("[consumer-%v] flushing %d words to DB\n", partitionID, len(events))
	for _, event := range events {
		query := `INSERT INTO processed_events(event_id) VALUES ($1)
		ON CONFLICT (event_id) DO NOTHING RETURNING event_id`
		err := tx.QueryRowContext(ctx, query, event.Id).Scan(&event.Id)

		if err != nil {
			// if no row was returned, event was already processed.
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}

			return fmt.Errorf("insert event %s: %w", event.Id, err)
		}

		if _, err := stmt.ExecContext(ctx, event.Word, event.Count); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec word %q: %w", event.Word, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	log.Printf("[consumer-%v] flushed %d words to DB\n", partitionID, len(events))
	return nil
}

func (c *Client) processTuple(partitionID int, b []byte) ([]types.TupleEvent, error) {
	var events []types.TupleEvent
	if err := json.Unmarshal(b, &events); err != nil {
		return events, fmt.Errorf("unmarshal: %w", err)
	}

	log.Printf("[consumer-%d] tupleEvent processed", partitionID)

	// Q! why are we actually having urlHash in msg event ? do we need it ?
	// tupleBatch[t.Word] += t.Count // default value is 0
	return events, nil
}

func partitionSubject(id int) string {
	return fmt.Sprintf("%s.%d", streamName, id)
}

func consumerName(id int) string {
	return fmt.Sprintf("tuple-indexer-%d", id)
}
