# Quick Reference Guide

Fast answers for common questions and tasks.

## TL;DR - Get Started in 5 Minutes

```bash
# 1. Update dependencies
cp go_mod_updated.txt go.mod
go mod tidy

# 2. Setup files
mkdir -p cloudflare db badger_db
cp cloudflare_*.go cloudflare/
cp db_*.go db/

# 3. Configure
cp .env.example .env
# Edit .env with your Cloudflare credentials

# 4. Run local crawler (test)
export CRAWLER_ENGINE=local
go run main.go

# 5. Run Cloudflare crawler
export CRAWLER_ENGINE=cloudflare
go run main.go
```

## Switching Crawlers

### To Use Local Crawler
```bash
export CRAWLER_ENGINE=local
# OR don't set it (default)
go run main.go
```

### To Use Cloudflare Crawler
```bash
export CRAWLER_ENGINE=cloudflare
export CLOUDFLARE_API_TOKEN=your_token
export CLOUDFLARE_ACCOUNT_ID=your_account_id
go run main.go
```

### In Code (main.go)
```go
// Automatic selection based on CRAWLER_ENGINE env var
crawler, err := getCrawlerEngine(crawlerEngine, rdb, bfKey)
```

## Common Commands

### Test Redis Connection
```bash
redis-cli ping
# Expected: PONG
```

### View Redis Bloom Filter
```bash
redis-cli
> SCAN 0 MATCH "wiki_bf*"
> BF.INFO wiki_bf_2025
```

### View BadgerDB Statistics
```bash
# In Go code:
bs, _ := db.NewBadgerStore("./badger_db")
stats := bs.GetStats()
fmt.Printf("Pages: %d\nSize: %d bytes\n", stats["totalPages"], stats["totalCompressedSize"])
bs.Close()
```

### Test Cloudflare API Connection
```bash
curl -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID" \
  -X GET
```

### Run with Custom Configuration
```bash
# Override environment variables on command line
CRAWLER_ENGINE=cloudflare \
CLOUDFLARE_POLL_INTERVAL=10 \
CLOUDFLARE_MAX_WAIT_TIME=3600 \
CRAWLER_WORKERS=4 \
go run main.go
```

### Build Executable
```bash
go build -o crawler main.go
./crawler
```

### Run with Race Detection
```bash
go run -race main.go
```

## File Locations

| Component | File | Location |
|-----------|------|----------|
| Main app | main_refactored.go | `./` → becomes `main.go` |
| Local crawler | LocalCrawler | `main.go` |
| Cloudflare types | cloudflare_types.go | `cloudflare/types.go` |
| Cloudflare client | cloudflare_client.go | `cloudflare/client.go` |
| Cloudflare orchestrator | cloudflare_crawler.go | `cloudflare/crawler.go` |
| Redis operations | db_redis.go | `db/redis.go` |
| Storage operations | db_badger.go | `db/badger.go` |
| Data storage | (auto-created) | `./badger_db/` |
| Configuration | .env.example | `./.env` |

## Environment Variables

### Crawler Selection
- `CRAWLER_ENGINE` - `local` (default) or `cloudflare`

### Local Crawler Config
- `CRAWLER_WORKERS` - Number of concurrent workers (default: 8)
- `CRAWLER_POLITENESS_MS` - Delay between requests in ms (default: 800)

### Cloudflare Crawler Config
- `CLOUDFLARE_API_TOKEN` - Bearer token for authentication (required)
- `CLOUDFLARE_ACCOUNT_ID` - Your Cloudflare account ID (required)
- `CLOUDFLARE_POLL_INTERVAL` - Status check interval in seconds (default: 15)
- `CLOUDFLARE_MAX_WAIT_TIME` - Max job wait in seconds (default: 604800)

### Seed URLs
- `ALLOWED_DOMAINS` - Comma-separated URLs or domains (overrides defaults)

### Other
- `REDIS_HOST` - Redis host (default: localhost)
- `REDIS_PORT` - Redis port (default: 6379)
- `BADGER_DB_PATH` - BadgerDB directory (default: ./badger_db)
- `LOG_LEVEL` - Logging level (info, debug, error)

## API Endpoints

### Cloudflare Browser Rendering API

**Base URL:** `https://api.cloudflare.com/client/v4`

**Initiate Crawl**
```
POST /accounts/{accountId}/ai/run/@cf/workers-ai/crawler
Authorization: Bearer {token}
Content-Type: application/json

{
  "url": "https://example.com",
  "returnFormat": "markdown"
}

Response: { "result": { "id": "job-uuid" } }
```

**Check Status**
```
GET /accounts/{accountId}/ai/run/@cf/workers-ai/crawler/{jobId}
Authorization: Bearer {token}

Response: { "result": { "status": "completed", "pageCount": 150 } }
```

**Retrieve Results**
```
GET /accounts/{accountId}/ai/run/@cf/workers-ai/crawler/{jobId}/results
Authorization: Bearer {token}

Response: { "result": { "records": [...], "cursor": "next-page" } }
```

## Debugging

### Enable Verbose Logging
```bash
# In code, check log output for:
# - "Crawl job initiated with ID: xxx"
# - "Job xxx status: running"
# - "Retrieved N results"
# - "Crawl complete: X new pages stored"
```

### Check Job Status Manually
```bash
ACCOUNT_ID=your_account_id
JOB_ID=job_uuid_here
TOKEN=your_api_token

curl -H "Authorization: Bearer $TOKEN" \
  "https://api.cloudflare.com/client/v4/accounts/$ACCOUNT_ID/ai/run/@cf/workers-ai/crawler/$JOB_ID"
```

### View Crawler Metrics
```bash
# Count entries in Redis Bloom Filter
redis-cli BF.INFO wiki_bf_2025

# Check BadgerDB size
du -sh ./badger_db/

# List BadgerDB files
ls -lh badger_db/
```

### Test Page Retrieval from BadgerDB
```go
// Add this to main.go temporarily
bs, _ := db.NewBadgerStore("./badger_db")
defer bs.Close()

// List first page
urls := []string{}
err := bs.db.View(func(txn *badger.Txn) error {
    it := txn.NewIterator(badger.DefaultIteratorOptions)
    defer it.Close()
    for it.Seek([]byte("meta:")); it.ValidForPrefix([]byte("meta:")); it.Next() {
        // Process metadata...
    }
    return nil
})
```

## Performance Tuning

### Maximize Throughput (Local)
```bash
CRAWLER_ENGINE=local
CRAWLER_WORKERS=16
CRAWLER_POLITENESS_MS=500
go run main.go
```

### Minimize Resource Usage (Local)
```bash
CRAWLER_ENGINE=local
CRAWLER_WORKERS=2
CRAWLER_POLITENESS_MS=2000
go run main.go
```

### Fast Feedback (Cloudflare)
```bash
CLOUDFLARE_POLL_INTERVAL=5
go run main.go
```

### Efficient Polling (Cloudflare)
```bash
CLOUDFLARE_POLL_INTERVAL=60
go run main.go
```

## Common Error Messages & Fixes

| Error | Cause | Fix |
|-------|-------|-----|
| `Failed to connect to Redis: connection refused` | Redis not running | Start Redis: `redis-server` |
| `CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID must be set` | Missing credentials | Set in `.env` or export vars |
| `job not found (may have expired)` | Job older than 7 days | Reduce `CLOUDFLARE_MAX_WAIT_TIME` |
| `Badger is already in use` | Another process accessing DB | Close other crawler instances |
| `status 429` (rate limited) | Too many requests | Increase `CLOUDFLARE_POLL_INTERVAL` |
| `status 401` (unauthorized) | Bad API token | Verify token in Cloudflare dashboard |
| `context deadline exceeded` | Operation took too long | Increase timeout durations |

## Code Snippets

### Initialize Crawler Manually
```go
// Local
rdb, _ := db.RedisInit(ctx)
crawler := NewLocalCrawler(8, 800*time.Millisecond, rdb, "wiki_bf_2025")
crawler.Crawl(ctx, urls)

// Cloudflare
config := cfcrawler.CrawlerConfig{
    APIToken: "token",
    AccountID: "account",
    PollInterval: 15*time.Second,
    MaxWaitTime: 7*24*time.Hour,
}
crawler := cfcrawler.NewCrawler(config, rdb, "wiki_bf_2025")
crawler.Crawl(ctx, urls)
```

### Query Stored Pages
```go
bs, _ := db.NewBadgerStore("./badger_db")
content, metadata, _ := bs.RetrievePage("sha256hash")
fmt.Printf("Title: %s\nCrawled: %s\nSize: %d bytes\n",
    metadata.Title,
    metadata.CrawledAt,
    len(content))
```

### Check Deduplication
```go
// Check if URL seen
seen := db.SeenBefore(ctx, rdb, "wiki_bf_2025", "https://example.com")
fmt.Printf("Already crawled: %v\n", seen)

// Mark as seen
db.MarkSeen(ctx, rdb, "wiki_bf_2025", "https://example.com")
```

### Get Database Statistics
```go
bs, _ := db.NewBadgerStore("./badger_db")
stats := bs.GetStats()
fmt.Printf("Total pages: %d\n", stats["totalPages"])
fmt.Printf("Compressed: %d bytes\n", stats["totalCompressedSize"])
fmt.Printf("Original: %d bytes\n", stats["totalOriginalSize"])
ratio := stats["averageCompressionRatio"].(float64)
fmt.Printf("Ratio: %.1f%%\n", ratio*100)
```

## Monitoring Checklist

Daily:
- [ ] Crawler logs for errors
- [ ] Redis memory usage: `redis-cli INFO memory`
- [ ] BadgerDB size: `du -sh badger_db/`

Weekly:
- [ ] Total pages crawled
- [ ] Average response times
- [ ] Error rate

Monthly:
- [ ] Cloudflare API costs
- [ ] Update dependencies: `go get -u ./...`
- [ ] Backup BadgerDB data

## References

- **Redis Commands:** `redis-cli COMMAND DOCS`
- **BadgerDB Docs:** https://dgraph.io/docs/badger/
- **Cloudflare API:** https://developers.cloudflare.com/
- **Go Context:** https://golang.org/pkg/context/
- **Go HTTP:** https://golang.org/pkg/net/http/

## Quick Links

- **Main Guide:** See IMPLEMENTATION_GUIDE.md
- **Checklist:** See MIGRATION_CHECKLIST.md
- **Files Overview:** See FILES_SUMMARY.md
- **Full Plan:** See the original plan document

---

**Last Updated:** 2024-03-23  
**Version:** 1.0  

💡 **Tip:** Bookmark this page for quick access to commands and troubleshooting!
