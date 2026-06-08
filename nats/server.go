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
	NumPartitions  = 4
	batchSize      = 1000
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
		AckWait:       10 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		ReplayPolicy:  jetstream.ReplayInstantPolicy,
	})
	if err != nil {
		return fmt.Errorf("create consumer partition %d: %w", partitionID, err)
	}
	log.Printf("[consumer-%d] ready on subject %s", partitionID, partitionSubject(partitionID))

	// initilize the batch and count size
	tupleBatch := make(map[string]int, batchSize) // pre allocate the map of batchsize
	count := 0

	// starts consuming the tuple events
	for {
		select {
		case <-ctx.Done():
			// flush the remaining tuple
			if len(tupleBatch) > 0 {
				log.Printf("[consumer-%d] flushing the tuple batch", partitionID)
				if err := c.flushDB(tupleBatch); err != nil {
					log.Printf("flushDB FAILED: %v", err)
					continue
				}
				log.Printf("[consumer-%d] flushed - %d words", partitionID, len(tupleBatch))
				tupleBatch = make(map[string]int, batchSize)
				count = 0
			}
			log.Printf("[consumer-%d] shutting down", partitionID)
			return nil
		default:
			msgs, err := consumer.Fetch(10, jetstream.FetchMaxWait(2*time.Second))
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				continue
			}

			for msg := range msgs.Messages() {
				// process the tuple event and add it into the map & update count
				updatedCount, err := c.processTuple(partitionID, msg.Data(), tupleBatch, count)
				if err != nil {
					log.Printf("[consumer-%d] processing error: %v — nacking", partitionID, err)
					msg.Nak()
					continue
				}
				count = updatedCount // update the count
				// msg.Ack();

				// flush it into DB
				if count >= batchSize {
					log.Printf("[consumer-%d] count=%d mapLen=%v\n\n", partitionID, count, len(tupleBatch))
					// flush
					if err := c.flushDB(tupleBatch); err != nil {
						log.Printf("flush failed: %v", err)
						msg.Nak()
						continue

					}
					// reset
					tupleBatch = make(map[string]int, batchSize)
					count = 0
				}

				msg.Ack()
			}
		}
	}
}

func (c *Client) flushDB(batch map[string]int) error {
	tx, err := c.pg.Begin();
	if err != nil {
		return err
	}

	query := `INSERT INTO word_counts(word, count) VALUES ($1, $2) ON CONFLICT (word) DO UPDATE SET count = word_counts.count + EXCLUDED.count`;
	
	log.Printf("flushing %d records to DB", len(batch))

	for word, count := range batch {
		_, err := tx.Exec(query, word, count)

		if err != nil {
			tx.Rollback()
			return err
		}
	}
	log.Printf("flushed %d records to DB", len(batch))
	return tx.Commit()
}

func (c *Client) processTuple(partitionID int, b []byte, tupleBatch map[string]int, count int) (int, error) {
	var t types.TupleEvent
	if err := json.Unmarshal(b, &t); err != nil {
		return count, fmt.Errorf("unmarshal: %w", err)
	}

	log.Printf("[consumer-%d] word=%s freq=%d url=%s", partitionID, t.Word, t.Count, t.URLHash)

	// Q! why are we actually having urlHash in msg event ? do we need it ?
	tupleBatch[t.Word] += t.Count // default value is 0
	count++

	return count, nil
}

func partitionSubject(id int) string {
	return fmt.Sprintf("%s.%d", streamName, id)
}

func consumerName(id int) string {
	return fmt.Sprintf("tuple-indexer-%d", id)
}
