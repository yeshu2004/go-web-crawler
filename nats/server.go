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
	flushMaxRetry = 3
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

	// starts consuming the tuple events
	for {
		select {
		case <-ctx.Done():
			// flush the remaining tuple
			if len(tupleBatch) > 0 {
				// TODO: maybe if the flush fail we could retry the flush by two ways, i.e first-> 
				// trying to flush it again by the same consumer i.e. pausing the consumer for new tuple
				// and retring the existing faild tuple again.... and if it still fails by trying a max retry
				// limit, we could push the tuple to DLQ (which ofc has to be created) OR
				// second approch -> the we could push the failed tuple to a new rety queue and dont not disturb
				// the current consumer workflow.... and then the rety queue will try that after some time (i.e delay)
				// but again i see a race condition i.e. if retry queue and consumer queue try to update the word 
				// "nice" freq concurrently, then one would over write the other and thus creating a problem,
				// so maybe we could try to flush those retry queue consumed tuple when all the consumers are inactive
				// i.e only when either they dont have any new tuple to consume or they are shut down i.e ctrl+c, at 
				// that moment we can try to flush the failed tuple....and if still failed we could move them to DLQ
				// after certian max retry limit e.g. retry_limit = 3

				log.Printf("[consumer-%d] shutdown: flushing %d words", partitionID, len(tupleBatch))
				c.flushWithRetry(context.Background(), tupleBatch, partitionID);
				log.Printf("[consumer-%d] flushed - %d words", partitionID, len(tupleBatch))
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
				if err := c.processTuple(partitionID, msg.Data(), tupleBatch); err != nil {
					log.Printf("[consumer-%d] processing error: %v — nacking", partitionID, err)
					msg.Nak()
					continue
				}
				// msg.Ack();

				// flush it into DB
				if len(tupleBatch) >= batchSize {
					log.Printf("[consumer-%d] flushing %d words", partitionID, len(tupleBatch))
					// flush
					if err := c.flushDB(ctx, tupleBatch); err != nil {
						log.Printf("[consumer-%d] flush failed: %v", partitionID, err)
						msg.Nak()
						continue

					}
					// reset
					tupleBatch = make(map[string]int, batchSize)
				}

				msg.Ack()
			}
		}
	}
}

func (c *Client) flushWithRetry(ctx context.Context,batch map[string]int, partitionID int) {
	for i :=1 ; i< flushMaxRetry; i++{
		if err := c.flushDB(ctx, batch); err != nil{
			log.Printf("[consumer-%d] flush attempt %d/%d failed: %v", partitionID, i, flushMaxRetry, err);

			if i == flushMaxRetry {
				// TODO: push the batch into the DLQ OR Re-enqueue to a dedicated 
				// NATS retry subject with exponential back-off; the retry 
				// consumer only runs when the main consumers are quiescent 
				// to avoid the same race condition.
				log.Printf("[consumer-%d] giving up after %d attempts — %d words may be lost", partitionID, flushMaxRetry, len(batch))
			}
			continue;
		}
		// If no error...
		log.Printf("[consumer-%d] shutdown flush succeeded (%d words)", partitionID, len(batch))
		return;
	}
}

func (c *Client) flushDB(ctx context.Context, batch map[string]int) error {
	tx, err := c.pg.BeginTx(ctx, nil);
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	query := `INSERT INTO word_counts(word, count) VALUES ($1, $2) ON CONFLICT (word) DO UPDATE SET count = word_counts.count + EXCLUDED.count`;
	log.Printf("flushing %d records to DB", len(batch))

	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	log.Printf("flushing %d words to DB", len(batch))
	for word, count := range batch {
		if _, err := stmt.ExecContext(ctx, word, count); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec word %q: %w", word, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	
	log.Printf("flushed %d words to DB", len(batch))
	return nil
}

func (c *Client) processTuple(partitionID int, b []byte, tupleBatch map[string]int) error {
	var t types.TupleEvent
	if err := json.Unmarshal(b, &t); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	log.Printf("[consumer-%d] word=%s freq=%d url=%s", partitionID, t.Word, t.Count, t.URLHash)

	// Q! why are we actually having urlHash in msg event ? do we need it ?
	tupleBatch[t.Word] += t.Count // default value is 0

	return nil
}

func partitionSubject(id int) string {
	return fmt.Sprintf("%s.%d", streamName, id)
}

func consumerName(id int) string {
	return fmt.Sprintf("tuple-indexer-%d", id)
}
