package indexer

import (
	"context"
	"log"
	"sync"
	"time"

	"github/yeshu2004/go-epics/pkg/database"
	"github/yeshu2004/go-epics/pkg/models"
)

type postingKey struct {
	term    string
	urlHash string
}

type aggregatedPosting struct {
	term    string
	urlHash string
	url     string
	freq    int
	seenAt  time.Time
}

type Batcher struct {
	repo          *database.PostgresRepository
	maxEvents     int
	flushInterval time.Duration

	mu         sync.Mutex
	eventCount int
	postings   map[postingKey]*aggregatedPosting
}

func NewBatcher(repo *database.PostgresRepository, maxEvents int, flushInterval time.Duration) *Batcher {
	if maxEvents <= 0 {
		maxEvents = 10000
	}
	if flushInterval <= 0 {
		flushInterval = 5 * time.Second
	}
	return &Batcher{
		repo:          repo,
		maxEvents:     maxEvents,
		flushInterval: flushInterval,
		postings:      make(map[postingKey]*aggregatedPosting),
	}
}

func (b *Batcher) Add(ctx context.Context, event *models.PostingEvent) error {
	if event == nil {
		return nil
	}

	b.mu.Lock()
	for term, freq := range event.Terms {
		if freq <= 0 {
			continue
		}
		key := postingKey{term: term, urlHash: event.URLHash}
		agg, ok := b.postings[key]
		if !ok {
			agg = &aggregatedPosting{
				term:    term,
				urlHash: event.URLHash,
				url:     event.URL,
				seenAt:  event.CrawledAt,
			}
			b.postings[key] = agg
		}
		agg.freq += freq
	}
	b.eventCount++
	shouldFlush := b.eventCount >= b.maxEvents
	b.mu.Unlock()

	if shouldFlush {
		return b.Flush(ctx)
	}
	return nil
}

func (b *Batcher) RunPeriodicFlush(ctx context.Context) {
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if err := b.Flush(context.Background()); err != nil {
				log.Printf("final flush failed: %v", err)
			}
			return
		case <-ticker.C:
			if err := b.Flush(ctx); err != nil {
				log.Printf("periodic flush failed: %v", err)
			}
		}
	}
}

func (b *Batcher) Flush(ctx context.Context) error {
	b.mu.Lock()
	if len(b.postings) == 0 {
		b.eventCount = 0
		b.mu.Unlock()
		return nil
	}

	rows := make([]database.PostingRow, 0, len(b.postings))
	for _, posting := range b.postings {
		rows = append(rows, database.PostingRow{
			Term:    posting.term,
			URLHash: posting.urlHash,
			URL:     posting.url,
			Freq:    posting.freq,
			SeenAt:  posting.seenAt,
		})
	}
	b.postings = make(map[postingKey]*aggregatedPosting)
	b.eventCount = 0
	b.mu.Unlock()

	return b.repo.UpsertPostings(ctx, rows)
}
