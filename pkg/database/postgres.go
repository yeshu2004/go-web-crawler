package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostingRow struct {
	Term    string
	URLHash string
	URL     string
	Freq    int
	SeenAt  time.Time
}

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(ctx context.Context, connString string) (*PostgresRepository, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, err
	}
	repo := &PostgresRepository{pool: pool}
	if err := repo.initSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return repo, nil
}

func (r *PostgresRepository) initSchema(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL,
			url_hash TEXT NOT NULL,
			url TEXT NOT NULL,
			frequency INTEGER NOT NULL DEFAULT 0,
			first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (term, url_hash)
		);
		CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term);
	`)
	return err
}

func (r *PostgresRepository) UpsertPostings(ctx context.Context, rows []PostingRow) error {
	if len(rows) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(`
			INSERT INTO postings (term, url_hash, url, frequency, first_seen, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (term, url_hash)
			DO UPDATE SET
				frequency = postings.frequency + EXCLUDED.frequency,
				updated_at = now(),
				url = EXCLUDED.url
		`, row.Term, row.URLHash, row.URL, row.Freq, row.SeenAt)
	}

	results := r.pool.SendBatch(ctx, batch)
	defer results.Close()
	for i := 0; i < len(rows); i++ {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("batch upsert failed at item %d: %w", i, err)
		}
	}
	return nil
}

func (r *PostgresRepository) Close() {
	r.pool.Close()
}
