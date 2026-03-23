package cloudflare

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	db "github/yeshu2004/go-epics/db"

	"github.com/redis/go-redis/v9"
)

// Crawler orchestrates Cloudflare-based crawling with polling and deduplication.
type Crawler struct {
	client     *Client
	redisDB    *redis.Client
	badgerDB   interface{} // TODO: Replace with actual BadgerDB client
	config     CrawlerConfig
	bloomKey   string
	maxWaitCtx context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
}

// NewCrawler creates a new Cloudflare crawler instance.
func NewCrawler(cfg CrawlerConfig, redisClient *redis.Client, bloomKey string) *Crawler {
	maxWaitCtx, cancel := context.WithTimeout(context.Background(), cfg.MaxWaitTime)
	return &Crawler{
		client:     NewClient(cfg),
		redisDB:    redisClient,
		config:     cfg,
		bloomKey:   bloomKey,
		maxWaitCtx: maxWaitCtx,
		cancel:     cancel,
	}
}

// Crawl initiates and orchestrates a crawl job.
func (c *Crawler) Crawl(ctx context.Context, urls []string) error {
	if len(urls) == 0 {
		return fmt.Errorf("no URLs provided for crawling")
	}

	// Cloudflare API expects a single starting URL
	// For now, crawl the first URL; support for multiple seed URLs can be added
	startURL := urls[0]

	log.Printf("Starting Cloudflare crawl for: %s", startURL)

	crawlReq := CrawlRequest{
		URL:            startURL,
		ReturnFormat:   c.config.ReturnFormat,
		MaxPages:       c.config.MaxPages,
		AllowedDomains: c.config.AllowedDomains,
	}

	jobID, err := c.client.InitiateCrawl(ctx, crawlReq)
	if err != nil {
		return fmt.Errorf("failed to initiate crawl: %w", err)
	}

	// Poll for job completion
	jobStatus, err := c.pollForCompletion(ctx, jobID)
	if err != nil {
		return fmt.Errorf("polling failed: %w", err)
	}

	log.Printf("Crawl job %s completed. Pages crawled: %d, Errors: %d", jobID, jobStatus.PageCount, jobStatus.ErrorCount)

	// Retrieve and process results
	duplicatesSkipped := 0
	pagesCrawled := 0
	cursor := ""

	for {
		results, err := c.client.RetrieveResults(c.maxWaitCtx, jobID, cursor)
		if err != nil {
			log.Printf("Warning: Failed to retrieve some results: %v", err)
			break
		}

		// Process each crawled page
		for _, page := range results.Records {
			if page.StatusCode != 200 && page.StatusCode != 0 {
				log.Printf("Skipping %s (status %d)", page.URL, page.StatusCode)
				continue
			}

			// Check for duplicates using Bloom Filter
			if c.seenBefore(ctx, page.URL) {
				duplicatesSkipped++
				continue
			}

			// Mark as seen and store
			c.markSeen(ctx, page.URL)
			if err := c.storePage(ctx, page); err != nil {
				log.Printf("Failed to store page %s: %v", page.URL, err)
				continue
			}
			pagesCrawled++
		}

		// Check for more results
		if results.Cursor == "" {
			break
		}
		cursor = results.Cursor
	}

	log.Printf("Crawl complete: %d new pages stored, %d duplicates skipped", pagesCrawled, duplicatesSkipped)
	return nil
}

// pollForCompletion polls the job status until completion or timeout.
func (c *Crawler) pollForCompletion(ctx context.Context, jobID string) (*JobStatus, error) {
	ticker := time.NewTicker(c.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.maxWaitCtx.Done():
			// Max wait time exceeded; attempt graceful cancellation
			_ = c.client.CancelJob(context.Background(), jobID)
			return nil, fmt.Errorf("crawl job exceeded max wait time (%v)", c.config.MaxWaitTime)
		case <-ticker.C:
			status, err := c.client.PollJobStatus(ctx, jobID)
			if err != nil {
				// If job not found, it may have expired; attempt to retrieve results anyway
				if isJobExpiredError(err) {
					log.Printf("Warning: Job %s may have expired, attempting to retrieve results", jobID)
					return &JobStatus{
						ID:        jobID,
						Status:    "expired",
						PageCount: 0,
					}, nil
				}
				return nil, err
			}

			// Check if job is complete
			switch status.Status {
			case "completed", "succeeded":
				log.Printf("Job %s completed successfully", jobID)
				return status, nil
			case "failed":
				return nil, fmt.Errorf("crawl job failed: %s", status.FailureReasonPublic)
			case "pending", "running":
				log.Printf("Job %s still running, will check again in %v", jobID, c.config.PollInterval)
				continue
			}
		}
	}
}

// storePage stores a crawled page in BadgerDB (or placeholder for now).
func (c *Crawler) storePage(_ context.Context, page CrawledPage) error {
	// TODO: Implement actual BadgerDB storage
	// For now, just log the storage attempt

	contentLength := len(page.Markdown)
	if page.Markdown == "" {
		contentLength = len(page.HTML)
	}

	log.Printf("Would store: %s (title: %s, content length: %d bytes)",
		page.URL, page.Title, contentLength)

	// Placeholder implementation:
	// - Hash the URL (SHA256, already done in redis.go)
	// - Compress the content (gzip)
	// - Store key-value pair in BadgerDB
	// - Metadata (status code, title, etc.) stored alongside or separately

	return nil
}

// seenBefore checks if a URL has been crawled before using the Bloom Filter.
func (c *Crawler) seenBefore(ctx context.Context, url string) bool {
	return db.SeenBefore(ctx, c.redisDB, c.bloomKey, url)
}

// markSeen marks a URL as seen in the Bloom Filter.
func (c *Crawler) markSeen(ctx context.Context, url string) {
	db.MarkSeen(ctx, c.redisDB, c.bloomKey, url)
}

// isJobExpiredError checks if an error indicates the job has expired.
func isJobExpiredError(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return msg == "job not found (may have expired)" ||
		msg == "job results not found (job may have expired)"
}

// Close cleans up resources.
func (c *Crawler) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}

	// Close Redis and BadgerDB connections as needed
	// c.redisDB.Close() // Keep this open if managed elsewhere
	return nil
}

// GetStatus retrieves the current status of a crawl job.
func (c *Crawler) GetStatus(ctx context.Context, jobID string) (*JobStatus, error) {
	return c.client.PollJobStatus(ctx, jobID)
}

// CancelCrawl cancels an ongoing crawl job.
func (c *Crawler) CancelCrawl(ctx context.Context, jobID string) error {
	return c.client.CancelJob(ctx, jobID)
}
