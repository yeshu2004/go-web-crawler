# Web Crawler: Cloudflare Integration & Pluggable Architecture Guide

## Overview

This implementation adds support for **Cloudflare Browser Rendering API** as an alternative to your existing local worker-pool crawler. The refactored architecture introduces a pluggable crawler pattern, allowing you to switch between engines via environment configuration.

## Architecture

### Key Design Pattern: Pluggable Crawlers

The new architecture implements a simple interface-based pattern:

```go
type Crawler interface {
    Crawl(ctx context.Context, urls []string) error
    Close() error
}
```

Two implementations are provided:

1. **LocalCrawler** - Your original worker pool approach (BFS, Redis Bloom Filter deduplication)
2. **CloudflareCrawler** - Cloudflare Browser Rendering API orchestration with job polling

### File Structure

```
├── main_refactored.go          # Refactored main with pluggable crawlers
├── cloudflare_types.go         # Type definitions for Cloudflare API
├── cloudflare_client.go        # HTTP client for Cloudflare API
├── cloudflare_crawler.go       # Cloudflare crawler orchestration
├── db_redis.go                 # Updated Redis with exported functions
├── db_badger.go                # BadgerDB storage for crawled pages
├── .env.example                # Configuration template
└── go_mod_updated.txt          # Updated dependencies
```

## Quick Start

### 1. Choose Your Crawler Engine

Set the `CRAWLER_ENGINE` environment variable:

```bash
# Use local worker pool (default)
export CRAWLER_ENGINE=local

# OR use Cloudflare Browser Rendering API
export CRAWLER_ENGINE=cloudflare
```

### 2. Configure Environment

Copy and customize `.env.example`:

```bash
cp .env.example .env
```

#### For Local Crawler:
```env
CRAWLER_ENGINE=local
CRAWLER_WORKERS=8
CRAWLER_POLITENESS_MS=800
```

#### For Cloudflare Crawler:
```env
CRAWLER_ENGINE=cloudflare
CLOUDFLARE_API_TOKEN=your_token_here
CLOUDFLARE_ACCOUNT_ID=your_account_id_here
CLOUDFLARE_POLL_INTERVAL=15
CLOUDFLARE_MAX_WAIT_TIME=604800  # 7 days
ALLOWED_DOMAINS=wikipedia.org
```

### 3. Install Dependencies

```bash
# Replace your existing go.mod with the updated version
cp go_mod_updated.txt go.mod

# Download dependencies
go mod download
```

### 4. Run the Crawler

```bash
# With local crawler (default)
go run main_refactored.go db/redis.go db/badger.go

# With Cloudflare crawler
export CRAWLER_ENGINE=cloudflare
go run main_refactored.go cloudflare_types.go cloudflare_client.go cloudflare_crawler.go db/redis.go db/badger.go
```

## Implementation Details

### LocalCrawler

**Features:**
- Multi-threaded crawling with configurable worker pool
- Politeness delays between requests
- Redis Bloom Filter for deduplication
- BFS traversal of links
- Graceful shutdown handling

**Configuration:**
- `CRAWLER_WORKERS` - Number of concurrent workers (default: 8)
- `CRAWLER_POLITENESS_MS` - Delay between requests per worker (default: 800ms)

**How it works:**
1. Seeds initial URLs in a channel-based queue
2. Worker goroutines consume URLs from the queue
3. Each worker fetches the page, extracts links
4. New links are deduplicated via Redis Bloom Filter
5. New unique links are added back to the queue

### CloudflareCrawler

**Features:**
- Single API call to initiate crawling
- Automatic job polling with configurable intervals
- Handles pagination for large result sets
- Redis Bloom Filter deduplication (same as local)
- BadgerDB storage with compression
- Graceful timeout handling (7 days max)
- Status tracking and error reporting

**Configuration:**
- `CLOUDFLARE_API_TOKEN` - Your Cloudflare API token (Bearer auth)
- `CLOUDFLARE_ACCOUNT_ID` - Your Cloudflare account ID
- `CLOUDFLARE_POLL_INTERVAL` - How often to check job status (seconds, default: 15)
- `CLOUDFLARE_MAX_WAIT_TIME` - Max wait time for job completion (seconds, default: 604800 = 7 days)

**How it works:**
1. User initiates crawl via `/crawl` API endpoint
2. Cloudflare returns a `jobId`
3. Crawler polls job status every `CLOUDFLARE_POLL_INTERVAL` seconds
4. When job completes, retrieves results with pagination support
5. For each page:
   - Check if URL is in Redis Bloom Filter (deduplication)
   - Mark as seen in Bloom Filter
   - Store to BadgerDB with compression
6. Handle long-running jobs with `CLOUDFLARE_MAX_WAIT_TIME` timeout

### Deduplication Strategy

Both crawlers use the same deduplication approach:

**Redis Bloom Filter:**
- Hash each URL with SHA256
- Check existence: `BFExists(bloomKey, hash)`
- Mark as seen: `BFAdd(bloomKey, hash)`
- False positive rate: 0.1% (configurable)

**Implementation:**
```go
// In db/redis.go
func SeenBefore(ctx context.Context, client *redis.Client, bloomKey, url string) bool
func MarkSeen(ctx context.Context, client *redis.Client, bloomKey, url string)
```

Both crawlers use these exported functions for consistency.

### Storage Strategy

#### BadgerDB (Key-Value Store)

**Purpose:** Persistent storage of crawled pages with compression

**Schema:**
```
page:{urlHash}  → [compressed HTML/Markdown content]
meta:{urlHash}  → [JSON metadata: URL, title, status code, timestamps, etc.]
```

**Compression:**
- Uses gzip for content compression
- Typical compression ratio: 10-20% of original size
- Metadata tracked: original size, compressed size

**Features:**
- Automatic value log files (32MB each)
- 8 goroutines for concurrent operations
- Statistics API for monitoring storage

**Usage in CloudflareCrawler:**
```go
bs := db.NewBadgerStore("./badger_db")
defer bs.Close()

// Store a page
err := bs.StorePage(urlHash, markdown, metadata)

// Retrieve a page
content, meta, err := bs.RetrievePage(urlHash)

// Check if page exists
exists := bs.PageExists(urlHash)

// Get statistics
stats := bs.GetStats()  // {totalPages, totalCompressedSize, ...}
```

## API Endpoints Reference

### Cloudflare Browser Rendering API

Implemented in `cloudflare_client.go`:

#### 1. Initiate Crawl (POST)
```
POST /accounts/{accountId}/ai/run/@cf/workers-ai/crawler
Content-Type: application/json

{
  "url": "https://example.com",
  "returnFormat": "markdown|html",
  "maxPages": 1000,
  "allowedDomains": ["example.com"],
  "limit": {
    "pageLimit": 1000,
    "depthLimit": 10,
    "timeoutMs": 30000
  }
}

Response:
{
  "success": true,
  "result": {
    "id": "job-uuid-here",
    "createdAt": "2024-03-23T10:00:00Z",
    "status": "pending"
  }
}
```

#### 2. Poll Job Status (GET)
```
GET /accounts/{accountId}/ai/run/@cf/workers-ai/crawler/{jobId}

Response:
{
  "success": true,
  "result": {
    "id": "job-uuid-here",
    "status": "completed|pending|running|failed",
    "pageCount": 150,
    "errorCount": 5,
    "completedAt": "2024-03-23T10:30:00Z",
    "expiresAt": "2024-03-30T10:00:00Z"
  }
}
```

#### 3. Retrieve Results (GET)
```
GET /accounts/{accountId}/ai/run/@cf/workers-ai/crawler/{jobId}/results?cursor=nextPageToken

Response:
{
  "success": true,
  "result": {
    "records": [
      {
        "url": "https://example.com/page",
        "statusCode": 200,
        "title": "Page Title",
        "markdown": "# Content",
        "links": ["https://example.com/other"],
        "crawledAt": "2024-03-23T10:15:00Z"
      }
    ],
    "cursor": "nextPageTokenForPagination"
  }
}
```

## Error Handling

### Local Crawler
- HTTP errors logged and skipped
- Network timeouts after 30 seconds
- Invalid HTML gracefully handled
- Queue full condition handled with default case

### Cloudflare Crawler
- API errors include status code and Cloudflare error message
- Job expiration (after 7 days) detected and handled
- Rate limiting respected via `Retry-After` header
- Connection timeouts after 30 seconds
- Invalid authentication fails fast with clear error

## Performance Considerations

### Local Crawler Advantages
- Low latency (direct HTTP requests)
- Cheap (no API costs)
- Controllable concurrency
- JavaScript not rendered (faster)

### Local Crawler Disadvantages
- Requires your infrastructure
- Single process scaling limit
- No JavaScript rendering
- Need to manage robots.txt, rate limiting manually

### Cloudflare Crawler Advantages
- Renders JavaScript (gets dynamic content)
- Cloudflare handles rate limiting
- Offloaded processing
- Automatic retry logic
- Results stored for 7 days

### Cloudflare Crawler Disadvantages
- API costs per crawl
- Max 7-day result availability
- Polling latency (status checks)
- Less control over crawl behavior

## Monitoring & Debugging

### Logging

Both crawlers log:
- URLs being processed
- Links extracted
- Deduplication statistics
- Error conditions

Example log output:
```
2024-03-23 10:15:30 Crawl job initiated with ID: abc-123-def
2024-03-23 10:15:45 Job abc-123-def status: running (pages: 45, errors: 2)
2024-03-23 10:16:00 Job abc-123-def status: running (pages: 120, errors: 5)
2024-03-23 10:16:15 Job abc-123-def status: completed (pages: 250, errors: 8)
2024-03-23 10:16:16 Retrieved 250 results for job abc-123-def
2024-03-23 10:16:30 Crawl complete: 230 new pages stored, 20 duplicates skipped
```

### Inspecting BadgerDB

```go
bs, _ := db.NewBadgerStore("./badger_db")
defer bs.Close()

// Get storage statistics
stats := bs.GetStats()
fmt.Printf("Total pages: %d\n", stats["totalPages"])
fmt.Printf("Compressed size: %d bytes\n", stats["totalCompressedSize"])
fmt.Printf("Original size: %d bytes\n", stats["totalOriginalSize"])
fmt.Printf("Compression ratio: %.2f%%\n", stats["averageCompressionRatio"].(float64)*100)
```

### Cloudflare Job Status Endpoint

Monitor active jobs directly:
```bash
curl -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/ai/run/@cf/workers-ai/crawler/job-id-here"
```

## Migration from Local to Cloudflare

**Step-by-step:**

1. Ensure your `.env` has Cloudflare credentials
2. Set `CRAWLER_ENGINE=cloudflare`
3. Optionally migrate existing BadgerDB:
   - Run local crawler once to populate
   - Switch to Cloudflare crawler
   - Both share Redis Bloom Filter (no conflicts)
4. Monitor first run with increased logging

**Backward compatibility:**
- Existing Redis data is reused
- BadgerDB schemas are compatible
- Graceful fallback to local if Cloudflare fails

## Troubleshooting

### "CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID must be set"
→ Check `.env` file has values and `CRAWLER_ENGINE=cloudflare` is set

### "job not found (may have expired)"
→ Job results expire after 7 days; increase `CLOUDFLARE_MAX_WAIT_TIME` if needed

### "failed to create HTTP request"
→ Check network connectivity and API endpoint URL

### Redis connection fails
→ Ensure Redis is running: `redis-cli ping`

### BadgerDB lock file error
→ Check no other process is accessing `./badger_db` directory

## Next Steps / TODOs

1. **Distributed Crawling:** Support multiple crawlers writing to shared BadgerDB
2. **Metrics Collection:** Prometheus metrics for crawl statistics
3. **Result Export:** Export crawled data to JSON/CSV
4. **Web Dashboard:** Monitor crawls in progress
5. **Robots.txt Parser:** Respect `robots.txt` for Cloudflare crawls
6. **Seed URL Management:** Database-backed seed URL management
7. **Resume Capability:** Resume interrupted crawls

## Code Examples

### Run Local Crawler
```go
ctx := context.Background()
rdb, _ := db.RedisInit(ctx)
crawler := NewLocalCrawler(8, 800*time.Millisecond, rdb, "wiki_bf_2025")
crawler.Crawl(ctx, []string{"https://en.wikipedia.org/wiki/Ramayana"})
crawler.Close()
```

### Run Cloudflare Crawler
```go
config := cfcrawler.CrawlerConfig{
    APIToken: "my-token",
    AccountID: "my-account",
    PollInterval: 15 * time.Second,
    MaxWaitTime: 7 * 24 * time.Hour,
}
crawler := cfcrawler.NewCrawler(config, rdb, "wiki_bf_2025")
crawler.Crawl(ctx, []string{"https://en.wikipedia.org/wiki/Ramayana"})
```

### Query Stored Pages
```go
bs, _ := db.NewBadgerStore("./badger_db")
content, meta, _ := bs.RetrievePage("sha256hash")
fmt.Printf("Title: %s\n", meta.Title)
fmt.Printf("Crawled: %s\n", meta.CrawledAt)
fmt.Printf("Content length: %d bytes\n", len(content))
```

## References

- [Cloudflare Browser Rendering API Docs](https://developers.cloudflare.com/workers-ai/models/llm/)
- [BadgerDB Documentation](https://dgraph.io/docs/badger/)
- [Redis Bloom Filter](https://redis.io/commands/bf.add/)
- [Go Context Package](https://golang.org/pkg/context/)

---

**Questions?** Check the inline comments in each file for detailed explanations of function behavior and design decisions.
