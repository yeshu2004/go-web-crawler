package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Client handles all HTTP communication with Cloudflare Browser Rendering API.
type Client struct {
	httpClient *http.Client
	config     CrawlerConfig
}

// NewClient creates a new Cloudflare API client.
func NewClient(config CrawlerConfig) *Client {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.cloudflare.com/client/v4"
	}
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		config: config,
	}
}

// InitiateCrawl starts a new crawl job and returns the job ID.
func (c *Client) InitiateCrawl(ctx context.Context, req CrawlRequest) (string, error) {
	if req.ReturnFormat == "" {
		req.ReturnFormat = "markdown"
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal crawl request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("%s/accounts/%s/ai/run/@cf/workers-ai/crawler", c.config.BaseURL, c.config.AccountID),
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	c.setAuthHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to execute crawl request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("cloudflare API error (status %d): %s", resp.StatusCode, string(body))
	}

	var crawlResp CrawlResponse
	if err := json.NewDecoder(resp.Body).Decode(&crawlResp); err != nil {
		return "", fmt.Errorf("failed to decode crawl response: %w", err)
	}

	if !crawlResp.Success {
		return "", fmt.Errorf("crawl initiation failed: %v", crawlResp.Errors)
	}

	log.Printf("Crawl job initiated with ID: %s", crawlResp.Result.ID)
	return crawlResp.Result.ID, nil
}

// PollJobStatus retrieves the current status of a crawl job.
func (c *Client) PollJobStatus(ctx context.Context, jobID string) (*JobStatus, error) {
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/accounts/%s/ai/run/@cf/workers-ai/crawler/%s", c.config.BaseURL, c.config.AccountID, jobID),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create status request: %w", err)
	}

	c.setAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to poll job status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("job not found (may have expired): %s", jobID)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cloudflare API error (status %d): %s", resp.StatusCode, string(body))
	}

	var statusResp CrawlStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, fmt.Errorf("failed to decode status response: %w", err)
	}

	if !statusResp.Success {
		return nil, fmt.Errorf("status poll failed: %v", statusResp.Errors)
	}

	log.Printf("Job %s status: %s (pages: %d, errors: %d)", jobID, statusResp.Result.Status, statusResp.Result.PageCount, statusResp.Result.ErrorCount)
	return &statusResp.Result, nil
}

// RetrieveResults fetches crawled pages from a completed job with optional pagination.
func (c *Client) RetrieveResults(ctx context.Context, jobID string, cursor string) (*ResultsData, error) {
	url := fmt.Sprintf("%s/accounts/%s/ai/run/@cf/workers-ai/crawler/%s/results", c.config.BaseURL, c.config.AccountID, jobID)
	if cursor != "" {
		url += "?cursor=" + cursor
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create results request: %w", err)
	}

	c.setAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("job results not found (job may have expired)")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cloudflare API error (status %d): %s", resp.StatusCode, string(body))
	}

	var resultsResp CrawlResultsResponse
	if err := json.NewDecoder(resp.Body).Decode(&resultsResp); err != nil {
		return nil, fmt.Errorf("failed to decode results response: %w", err)
	}

	if !resultsResp.Success {
		return nil, fmt.Errorf("results retrieval failed: %v", resultsResp.Errors)
	}

	log.Printf("Retrieved %d results for job %s", len(resultsResp.Result.Records), jobID)
	return &resultsResp.Result, nil
}

// CancelJob cancels a running crawl job (if supported by Cloudflare API).
func (c *Client) CancelJob(ctx context.Context, jobID string) error {
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodDelete,
		fmt.Sprintf("%s/accounts/%s/ai/run/@cf/workers-ai/crawler/%s", c.config.BaseURL, c.config.AccountID, jobID),
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to create cancel request: %w", err)
	}

	c.setAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to cancel job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to cancel job (status %d): %s", resp.StatusCode, string(body))
	}

	log.Printf("Job %s cancelled successfully", jobID)
	return nil
}

// setAuthHeaders adds Cloudflare authentication headers to the request.
func (c *Client) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.config.APIToken))
	req.Header.Set("User-Agent", "GoWebCrawler/1.0 (+https://github.com/yourname/go-epics)")
}

// HandleRateLimiting detects and handles rate limit responses gracefully.
// Returns the number of seconds to wait before retrying.
func HandleRateLimiting(resp *http.Response) (int, bool) {
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter != "" {
			var waitSeconds int
			_, err := fmt.Sscanf(retryAfter, "%d", &waitSeconds)
			if err == nil {
				return waitSeconds, true
			}
		}
		return 60, true // Default to 60 seconds if Retry-After not readable
	}
	return 0, false
}
