package cloudflare

import (
	"time"
)

// CrawlRequest represents the request payload for initiating a crawl job.
type CrawlRequest struct {
	URL          string            `json:"url"`
	ReturnFormat string            `json:"returnFormat"` // "markdown" or "html"
	MaxPages     int               `json:"maxPages,omitempty"`
	AllowedDomains []string        `json:"allowedDomains,omitempty"`
	Limit        *CrawlLimit       `json:"limit,omitempty"`
	Scrape       *ScrapingConfig   `json:"scrape,omitempty"`
}

// CrawlLimit defines constraints on the crawl job.
type CrawlLimit struct {
	PageLimit     int `json:"pageLimit,omitempty"`
	DepthLimit    int `json:"depthLimit,omitempty"`
	TimeoutMs     int `json:"timeoutMs,omitempty"`
}

// ScrapingConfig defines what to extract from pages.
type ScrapingConfig struct {
	Schema map[string]interface{} `json:"schema,omitempty"`
}

// CrawlResponse represents the response from initiating a crawl.
type CrawlResponse struct {
	Success bool     `json:"success"`
	Result  CrawlJob `json:"result"`
	Errors  []string `json:"errors,omitempty"`
}

// CrawlJob contains job metadata.
type CrawlJob struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	Status    string    `json:"status"` // "pending", "running", "completed", "failed"
	FailureReasonPublic string `json:"failureReasonPublic,omitempty"`
}

// CrawlStatusResponse represents the status response for a crawl job.
type CrawlStatusResponse struct {
	Success bool     `json:"success"`
	Result  JobStatus `json:"result"`
	Errors  []string `json:"errors,omitempty"`
}

// JobStatus provides detailed job status information.
type JobStatus struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
	StartedAt       time.Time `json:"startedAt,omitempty"`
	CompletedAt     time.Time `json:"completedAt,omitempty"`
	PageCount       int       `json:"pageCount"`
	ErrorCount      int       `json:"errorCount"`
	FailureReasonPublic string `json:"failureReasonPublic,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt,omitempty"` // Crawl results expire after 7 days
}

// CrawlResultsResponse represents the paginated results from a crawl.
type CrawlResultsResponse struct {
	Success bool          `json:"success"`
	Result  ResultsData   `json:"result"`
	Errors  []string      `json:"errors,omitempty"`
}

// ResultsData holds the actual crawl results.
type ResultsData struct {
	Records []CrawledPage `json:"records"`
	Cursor  string        `json:"cursor,omitempty"` // For pagination
}

// CrawledPage represents a single crawled page.
type CrawledPage struct {
	URL             string                 `json:"url"`
	StatusCode      int                    `json:"statusCode"`
	ContentType     string                 `json:"contentType"`
	Markdown        string                 `json:"markdown,omitempty"`
	HTML            string                 `json:"html,omitempty"`
	Title           string                 `json:"title,omitempty"`
	Description     string                 `json:"description,omitempty"`
	Error           string                 `json:"error,omitempty"`
	Links           []string               `json:"links,omitempty"`
	ExtractedData   map[string]interface{} `json:"extractedData,omitempty"`
	CrawledAt       time.Time              `json:"crawledAt,omitempty"`
}

// CrawlerConfig holds configuration for the Cloudflare crawler.
type CrawlerConfig struct {
	APIToken       string
	AccountID      string
	PollInterval   time.Duration // How often to check job status
	MaxWaitTime    time.Duration // Maximum time to wait for job completion (7 days default)
	BaseURL        string        // Cloudflare API base URL
	ReturnFormat   string        // "markdown" or "html"
	AllowedDomains []string      // Domains to limit crawling to
	MaxPages       int           // Maximum pages to crawl
}

// CrawlJobState tracks the state of a long-running crawl.
type CrawlJobState struct {
	JobID          string
	Status         string
	LastPollTime   time.Time
	CreatedAt      time.Time
	PagesCrawled   int
	ErrorCount     int
	ResultsCursor  string
}
