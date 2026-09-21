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
	flushMaxRetry  = 3
	flushInterval  = 5 * time.Second
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
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		ReplayPolicy:  jetstream.ReplayInstantPolicy,
	})
	if err != nil {
		return fmt.Errorf("create consumer partition %d: %w", partitionID, err)
	}
	log.Printf("[consumer-%d] ready on subject %s", partitionID, partitionSubject(partitionID))

	return c.consume(ctx, consumer, partitionID);
}


// main design is at-least-once NATS delivery with idempotent PostgreSQL processing,
// not literal exactly-once message delivery, but this gives once message delivery kinda... 
// because the event ID insert and word-count increment happen in the same transaction, a 
// redelivery after a successful DB commit won't increment the word again.
func (c *Client) consume(ctx context.Context, consumer jetstream.Consumer, partitionID int) error {
	// initilize the batch and count size
	// tupleBatch := make(map[string]int, batchSize) // pre allocate the map of batchsize
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

				err := c.flushBatch(flushCtx, pendingEvents, pendingMsgs, partitionID)
				cancel()

				if err != nil {
					log.Printf("consumer-%d] final flush failed: %v", partitionID, err)
					return err
				}

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
			}
			log.Printf("[consumer-%d] shutting down", partitionID)
			return nil
		default:
			msgs, err := consumer.Fetch(10, jetstream.FetchMaxWait(time.Second))
			if err == nil {
				for msg := range msgs.Messages() {
					// process the tuple event and add it into the map & update count
					events, err := c.processTuple(partitionID, msg.Data()); 
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
					// pendingEvents = append(pendingEvents, event);
					pendingMsgs = append(pendingMsgs, msg)
					pendingCount++
				}
			
			} else if !errors.Is(err, context.Canceled){
				 log.Printf("[consumer-%d] fetch error: %v", partitionID, err);
			}


			// flush if size threshold OR time threshold is reached.
			shouldFlush := pendingCount >= batchSize || (pendingCount > 0 && time.Since(lastFlush) >= flushInterval)

			if shouldFlush{
				log.Printf("[consumer-%d] flushing %d events, %d unique words",partitionID,pendingCount,len(pendingEvents))
				if err := c.flushBatch(ctx, pendingEvents, pendingMsgs, partitionID); err != nil{
					log.Printf("[consumer-%d] flush failed: %v",partitionID, err)
	
					// keep the batch in memory for another attempt, don't ACK these messages yet.
					continue
				}
	
				log.Printf("[consumer-%v] info: %v, %d, %v, %v", partitionID, pendingEvents, pendingCount, pendingMsgs, lastFlush)
	
				// tupleBatch = make(map[string]int, batchSize);
				pendingEvents = nil
				pendingMsgs = nil
				pendingCount = 0
				lastFlush = time.Time{}
			}

		}
	}
}


func (c *Client) flushBatch(ctx context.Context, events []types.TupleEvent, pending []jetstream.Msg, partitionID int) error {
	if len(events) == 0 {
		return nil
	}

	if err := c.flushDB(ctx, events, partitionID); err != nil {
		return err
	}

	for _, msg := range pending {
		if err := msg.Ack(); err != nil {
			log.Printf("[consumer-%d] ACK failed: %v", partitionID, err)
		}
	}

	return nil
}

// func (c *Client) flushWithRetry(ctx context.Context, batch map[string]int, partitionID int) {
// 	for i := 1; i <= flushMaxRetry; i++ {
// 		if err := c.flushDB(ctx, batch, partitionID); err != nil {
// 			log.Printf("[consumer-%d] flush attempt %d/%d failed: %v", partitionID, i, flushMaxRetry, err)
// 			if i == flushMaxRetry {
// 				// TODO: push the batch into the DLQ OR Re-enqueue to a dedicated
// 				// NATS retry subject with exponential back-off; the retry
// 				// consumer only runs when the main consumers are quiescent
// 				// to avoid the same race condition.
// 				log.Printf("[consumer-%d] giving up after %d attempts — %d words may be lost", partitionID, flushMaxRetry, len(batch))
// 			}
// 			continue
// 		}
// 		// If no error...
// 		log.Printf("[consumer-%d] shutdown flush succeeded (%d words)", partitionID, len(batch))
// 		return
// 	}
// }



// this is at-least-once delivery with effectively-once database effects, 
// assuming each published event has a stable, unique ID
func (c *Client) flushDB(ctx context.Context, events []types.TupleEvent, partitionID int) error {
	tx, err := c.pg.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback();

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

	// log.Printf("[consumer-%d] word=%s freq=%d url=%s", partitionID, t.Word, t.Count, t.URLHash)

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
