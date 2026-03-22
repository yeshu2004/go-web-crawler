# Architecture Overview

## System Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        MAIN APPLICATION                         │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ main.go                                                  │   │
│  │                                                          │   │
│  │  1. Load configuration from environment                 │   │
│  │  2. Initialize Redis & Bloom Filter                     │   │
│  │  3. Select crawler engine (local or cloudflare)         │   │
│  │  4. Start crawling                                      │   │
│  │  5. Graceful shutdown handler                           │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                              │
                    ┌─────────┴─────────┐
                    │                   │
        ┌───────────▼──────────┐   ┌──────────────────────┐
        │   LocalCrawler       │   │ CloudflareCrawler    │
        │   (Original Pool)    │   │ (API-based)          │
        └──────────┬───────────┘   └──────────┬───────────┘
                   │                          │
        ┌──────────▼──────────┐    ┌──────────▼──────────┐
        │  Worker Pool        │    │ Cloudflare API      │
        │  - 8 concurrent     │    │ - POST /crawl       │
        │  - BFS traversal    │    │ - Poll job status   │
        │  - Direct HTTP      │    │ - GET results       │
        └──────────┬──────────┘    └──────────┬──────────┘
                   │                          │
                   │                          │
        ┌──────────▼──────────────────────────▼──────────┐
        │       SHARED INFRASTRUCTURE LAYER              │
        │                                                │
        │  ┌─────────────────────────────────────────┐  │
        │  │  Redis Bloom Filter                     │  │
        │  │  - Deduplication (shared key)           │  │
        │  │  - SHA256 hash of URL                   │  │
        │  │  - 0.1% false positive rate             │  │
        │  └─────────────────────────────────────────┘  │
        │                                                │
        │  ┌─────────────────────────────────────────┐  │
        │  │  BadgerDB Storage                       │  │
        │  │  - page:{hash} → compressed content     │  │
        │  │  - meta:{hash} → JSON metadata          │  │
        │  │  - Automatic gzip compression           │  │
        │  │  - Transaction support                  │  │
        │  └─────────────────────────────────────────┘  │
        │                                                │
        └────────────────────────────────────────────────┘
```

## Data Flow

### LocalCrawler Flow

```
Seed URLs
   │
   ▼
Queue (channel)
   │
   ├─► Worker 1 ─┐
   ├─► Worker 2 ─┤
   ├─► Worker N ─┤
   │              ├─► Fetch (HTTP GET)
                  │   ▼
                  ├─► Parse HTML
                  │   ▼
                  ├─► Extract Links
                  │   ▼
                  └─► Check Bloom Filter
                      │
                      ├─► SEEN: Skip
                      │
                      └─► NEW: Mark in Bloom Filter
                          │
                          └─► Add to Queue (Loop)
```

### CloudflareCrawler Flow

```
Seed URL
   │
   ▼
POST /crawl API
   │
   ▼
Get Job ID
   │
   ▼
Poll Loop (every 15 seconds)
   │
   ├─► GET /status
   │    │
   │    ├─► "pending" / "running" → Sleep, retry
   │    │
   │    ├─► "completed" → Retrieve results
   │    │
   │    └─► "failed" → Error exit
   │
   ▼
GET /results (with pagination)
   │
   ├─► For each page:
   │   │
   │   ├─► Check Bloom Filter
   │   │
   │   ├─► SEEN: Skip
   │   │
   │   └─► NEW: Store to BadgerDB
   │       Mark in Bloom Filter
   │
   └─► If cursor present: Fetch next page
       │
       └─► Loop to "For each page"
```

## Component Interaction

```
┌─────────────┐         ┌──────────────┐         ┌──────────────┐
│   main.go   │         │cloudflare/   │         │     db/      │
│             │◄───────►│  client.go   │◄───────►│  redis.go    │
│ - Selection │         │              │         │  badger.go   │
│ - Setup     │         │ - HTTP calls │         │              │
│ - Shutdown  │         │ - Auth       │         │ - Bloom info │
└─────────────┘         │ - Polling    │         │ - Storage    │
       │                │ - Pagination │         └──────────────┘
       │                └──────────────┘
       │
       ▼
┌──────────────────────────────────────┐
│     Crawler Interface                │
│  - Crawl(ctx, urls) error           │
│  - Close() error                     │
└──────────────────────────────────────┘
       │                          │
       ├──────────────┬───────────┤
       │              │           │
       ▼              ▼           ▼
  LocalCrawler  CloudflareCrawler (implements Crawler)
```

## Deduplication Strategy

```
URL Input: "https://example.com/page"
   │
   ▼
SHA256 Hash: "a1b2c3d4e5f6..."
   │
   ▼
Redis Bloom Filter Check
   │
   ├─► EXISTS? → YES ──► SKIP
   │
   └─► EXISTS? → NO
       │
       ├─► Add to Bloom Filter
       │
       └─► PROCESS & STORE
```

## Storage Architecture

### BadgerDB Key Structure

```
Database: badger_db/

├── page:{url_hash}
│   └── Compressed HTML/Markdown content
│
├── meta:{url_hash}
│   └── {
│       "url": "https://...",
│       "title": "Page Title",
│       "statusCode": 200,
│       "crawledAt": "2024-03-23T10:00:00Z",
│       "sourceSize": 50000,
│       "compressedSize": 5000
│   }
│
└── ... (repeat for each URL)
```

### Compression Savings

```
Original Content: 50 KB
          │
          ▼
     Compress (gzip)
          │
          ▼
Compressed: 5 KB (10% of original)

Savings: 45 KB per page
Average across 1000 pages: 45 MB saved
```

## Configuration Flow

```
Environment Variables / .env file
          │
          ▼
getCrawlerEngine() reads CRAWLER_ENGINE
          │
    ┌─────┴─────┐
    │           │
    ▼           ▼
 "local"    "cloudflare"
    │           │
    ▼           ▼
LocalCrawler  CloudflareCrawler
  config        config
  ├─ workers    ├─ token
  ├─ politeness ├─ account
  └─ ...        └─ poll interval
```

## Redis Bloom Filter Usage

```
Operation: Check if URL seen

URL: "https://wikipedia.org/wiki/Ramayana"
   │
   ▼
SHA256: "8e4a7f2b9c1d..."
   │
   ▼
BFExists(bloom_key, hash)
   │
   ├─ False ──► URL is new
   │
   └─ True  ──► URL already crawled
```

## Error Handling Flow

```
Operation
   │
   ├─ Network Error
   │  └─► Log + Retry
   │
   ├─ HTTP Error (4xx, 5xx)
   │  └─► Log + Skip URL
   │
   ├─ API Rate Limit (429)
   │  └─► Read Retry-After
   │  └─► Wait + Retry
   │
   ├─ Authentication Error (401)
   │  └─► Fail immediately
   │  └─► Check credentials
   │
   ├─ Job Expired (>7 days)
   │  └─► Retrieve partial results
   │  └─► Log warning
   │
   └─ Context Cancelled
      └─► Graceful shutdown
```

## Performance Characteristics

### LocalCrawler

```
Configuration: 8 workers, 800ms politeness

Timeline:
0s     ► Start
5s     ► First batch processed (5 workers × 1 req)
10s    ► Pages growing exponentially
30s    ► ~15-20 pages crawled
1m     ► ~50+ pages crawled
5m     ► ~200+ pages crawled
15m    ► Slowing (approaching depth limit or domain limit)
```

### CloudflareCrawler

```
Timeline:
0s     ► Initiate crawl
5s     ► Job status: pending
15s    ► Job status: running (few pages)
30s    ► Job status: running (50+ pages)
60s    ► Job status: running (100+ pages)
120s   ► Job status: completed
125s   ► Results retrieved & stored

Total time: ~2 minutes for 100+ pages
```

## Concurrency Model

### LocalCrawler (Multi-threaded)

```
Main Thread     Worker 1    Worker 2    ... Worker 8
     │              │           │              │
     ├──► Queue ◄──┤           │              │
     │              ├─► HTTP ──┤              │
     │              ├─► Parse ─┤              │
     │              ├─ Fetch  ├──► Queue ◄──┤
     │              │           │              │
     └──► Close ◄──┴────────────┴──────────────┘
```

### CloudflareCrawler (Single-threaded polling)

```
Main Thread
     │
     ├─► POST /crawl
     │
     ├─► Polling Loop
     │    ├─► GET /status
     │    ├─► Sleep (15s)
     │    ├─► Repeat until complete
     │
     ├─► GET /results (with pagination)
     │    ├─► Process each page
     │    ├─► Store to BadgerDB
     │    ├─► Check for next page
     │
     └─► Close
```

## Security Flow

```
User Input (URL)
   │
   ▼
URL Validation
   ├─► Scheme check (http/https only)
   ├─► Host validation
   └─► Fragment removal
   │
   ▼
API Authentication
   ├─► Bearer token (from env)
   └─► HTTPS only
   │
   ▼
Storage
   ├─► No sensitive data logged
   ├─► Tokens not in database
   └─► .env excluded from git
```

---

This architecture ensures:
- ✅ Pluggable components (easy to extend)
- ✅ Shared state (consistent deduplication)
- ✅ Scalability (supports multiple crawlers)
- ✅ Persistence (BadgerDB storage)
- ✅ Error resilience (graceful degradation)
- ✅ Configuration flexibility (environment-based)
