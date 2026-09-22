# Go Distributed Web Crawler & Word-Frequency Indexer

A concurrent, horizontally-partitioned web crawler written in Go, built to explore how a crawl-and-index pipeline behaves under real distributed-systems constraints: backpressure, deduplication at scale, partitioned stream processing, and exactly-once-*effective* delivery over an at-least-once message bus.

Given one or more seed URLs, the system crawls a site breadth-first, extracts and counts words from every page, and durably aggregates a global word-frequency index in Postgres — while keeping every crawl instance isolated, every stage crash-recoverable, and every worker independently scalable.

> This isn't a `for` loop with a `visited map[string]bool`. It's built the way you'd actually have to build it once a single crawl produces more URLs than fit in memory, and once "did we already count this word" becomes a question across multiple processes instead of one.

---

## The core problem, and how each version got fixed

The commit history of this project *is* a distributed-systems debugging log. A few of the real problems hit and fixed along the way:

| Problem | Root cause | Fix |
|---|---|---|
| Crawler deadlocks after ~10k URLs | Workers blocked on `chan string <- link`; once the buffer filled, every worker was stuck *sending* and none were left to *receive* | Replaced the in-memory channel with **Redis as the frontier** — workers `BRPOP` from a durable queue instead of blocking on a Go channel |
| Same URL crawled twice under concurrent workers | Check-then-enqueue was two separate Redis calls — a classic TOCTOU race | Replaced with a **single atomic Lua script** that checks a Redis Bloom filter and pushes to the queue in one round trip |
| Word counts could be inflated on consumer redelivery | NATS JetStream is at-least-once; a redelivered message would double-count words | Each event carries a stable UUID; the consumer inserts it into a `processed_events` table **inside the same Postgres transaction** as the count update — a redelivery after a committed transaction becomes a no-op |
| One slow/failed DB flush could stall a whole partition forever | No isolation between "processing" and "durability" failures | Failed flushes retry with backoff, then get diverted to a **dead-letter stream (DLQ)** instead of blocking the consumer loop |

---

## Architecture

```
POST /run/crawler {"seed_url": [...]}
        │
        ▼
┌───────────────────┐      each request spins up an
│  Crawler Manager    │      independent Crawler{} with
│  (cancel-func map,   │      its own ID, Bloom filter key,
│   thread-safe)        │      and Redis queue — infra is
└─────────┬──────────┘      shared, state is isolated
          │
          ▼
┌────────────────────────────────────────────────────────┐
│   8 concurrent crawl workers (per crawler instance)       │
│                                                              │
│   BRPOP url ← Redis frontier queue                          │
│   GET url → HTML                                              │
│   extract text → word-frequency map                            │
│   gzip + store raw HTML  → BadgerDB (local KV, on disk)          │
│   extract <a href> links                                          │
│   for each link: atomic Lua(BF.EXISTS + BF.ADD + LPUSH)             │
└──────────────────────┬───────────────────────────────────────────┘
                        │ word buckets, hashed to 1-of-4 partitions
                        ▼
        ┌───────────────────────────────────┐
        │   NATS JetStream — TUPLE.0..TUPLE.3  │
        │   (durable, partitioned by word hash)  │
        └───────────────────┬───────────────────┘
                             ▼
        ┌─────────────────────────────────────────────┐
        │  4 parallel partition consumers                │
        │  batch by size (1000 msgs) OR time (5s)          │
        │  flush → Postgres, txn-scoped idempotency check    │
        │  on repeated flush failure → Dead Letter Queue        │
        └─────────────────────────────────────────────┘
```

---

## Engineering highlights

### 1. Per-request crawl isolation over shared infrastructure
Every `POST /run/crawler` spins up a brand-new `Crawler` with its own UUID, its own Redis-backed Bloom filter key, and its own frontier queue — while sharing the same Redis, BadgerDB, and NATS connections across all crawls. Multiple crawls can run concurrently without ever polluting each other's "visited" state.

### 2. Redis as the frontier — not a Go channel
Early versions used a buffered `chan string` as the link queue. It deadlocked: once the buffer filled, all 8 workers were blocked *pushing* new links and none were free to *pop* and process the backlog. The fix treats **Redis as the source of truth for pending work**, so the frontier can grow far past what fits in a channel buffer, and workers simply block on `BRPOP` with a timeout instead of a channel send.

### 3. Atomic check-and-enqueue via Lua
Deduplication needs to be exact under concurrency, so "is this URL new?" and "add it to the queue" happen as a **single atomic Redis Lua script** against a Bloom filter — eliminating the race where two workers both see a URL as "new" and enqueue it twice:
```lua
if redis.call("BF.EXISTS", KEYS[1], ARGV[1]) == 0 then
    redis.call("BF.ADD", KEYS[1], ARGV[1])
    redis.call("LPUSH", KEYS[2], ARGV[2])
    return 1
end
return 0
```

### 4. Consistent-hash partitioned stream processing
Extracted words aren't published one-by-one — they're bucketed by `sha256(word) % 4` into per-partition batches and published to dedicated NATS subjects (`TUPLE.0`–`TUPLE.3`). Four independent consumer goroutines each own one partition, so the same word is always handled by the same consumer, keeping word-count updates lock-free across consumers.

### 5. Effectively-once aggregation on top of at-least-once delivery
NATS JetStream guarantees *at-least-once* delivery — messages can be redelivered after a crash or a slow ACK. Rather than fight that, the consumer embeds a stable event ID and makes redelivery a safe no-op:
```sql
INSERT INTO processed_events(event_id) VALUES ($1)
ON CONFLICT (event_id) DO NOTHING RETURNING event_id;
-- only if a row was actually inserted:
INSERT INTO word_counts(word, count) VALUES ($1, $2)
ON CONFLICT (word) DO UPDATE SET count = word_counts.count + EXCLUDED.count;
```
Both statements run in the same transaction, so a message can be redelivered any number of times without ever double-counting a word.

### 6. Time- *and* size-based batch flushing with a DLQ safety net
Consumers batch up to 1000 messages or 5 seconds of accumulation (whichever comes first) before flushing to Postgres — trading a little latency for dramatically fewer round trips. If a flush fails after retries, the batch is pushed to a **dead-letter JetStream stream** instead of silently dropping data or wedging the consumer.

### 7. Graceful, cancellable, per-crawl shutdown
Each running crawl is tracked in a `CrawlerManager` keyed by ID, mapped to its own `context.CancelFunc`. Hitting `GET /shutdown/crawler?id=...` cancels just that crawl's workers — letting others keep running — and `context.Context` cancellation propagates all the way down to in-flight consumer batches, which flush what they have before exiting instead of losing in-memory state.

### 8. Compact on-disk archive of every crawled page
Every fetched page is gzip-compressed and written to an embedded **BadgerDB** LSM-tree store keyed by URL hash — a full local archive of the crawl, without needing S3 or a separate service for raw page storage.

---

## Tech stack

| Layer | Choice | Why |
|---|---|---|
| Language | Go | Goroutines + channels map naturally onto a fan-out crawl workload |
| HTTP | Standard library `net/http` | No framework needed for 3 routes; explicit control over the server lifecycle |
| Frontier / dedup | Redis (Lists + RedisBloom) | Atomic Lua scripting, `BRPOP` blocking pop, probabilistic membership at scale |
| Raw page storage | BadgerDB (embedded LSM KV store) | Fast local writes, no external dependency for archiving crawled HTML |
| Messaging | NATS JetStream (partitioned, durable) | At-least-once delivery, replay, and independent per-partition consumers |
| Aggregation store | PostgreSQL | Transactional idempotency (`ON CONFLICT`) for exactly-once *effective* counting |
| HTML parsing | `golang.org/x/net/html` | Streaming DOM walk for link + text extraction without a heavier parser |

---

## API

| Method | Route | Description |
|---|---|---|
| `POST` | `/run/crawler` | Body: `{"seed_url": ["https://..."]}`. Starts an isolated crawl, returns its `id` immediately (202 Accepted) |
| `GET` | `/shutdown/crawler?id={id}` | Cancels the running crawl with that ID |
| `GET` | `/` | Health check |

---

## Project structure

```
go-web-crawler/
├── main.go                  # HTTP server bootstrap, starts consumer goroutines
├── router/
│   ├── router.go              # HTTP handlers, infra wiring (Redis/Badger/NATS)
│   └── manager.go              # thread-safe registry of running crawls → cancel funcs
├── crawler/
│   └── crawl.go                 # worker pool, Bloom-filter dedup, word extraction, link parsing
├── nats/
│   └── server.go                  # JetStream streams, partitioned publish/consume, DLQ, batched flush
├── consumer/
│   └── consume.go                   # spins up one goroutine per partition consumer
├── db/
│   ├── reddis.go                     # Redis client + Bloom filter setup
│   └── postgres.go                    # Postgres connection pool
├── compress/
│   └── gzip.go                         # page compression for BadgerDB storage
├── reader/
│   └── read.go                          # standalone tool to dump BadgerDB pages back to .html
├── types/
│   └── type.go                           # shared TupleEvent wire type
└── middleware/
    └── middleware.go                      # CORS + request logging
```

---

## Running it locally

**Prerequisites:** Go 1.25+, Redis with the [RedisBloom](https://redis.io/docs/latest/develop/data-types/probabilistic/bloom-filter/) module, PostgreSQL, [NATS Server](https://docs.nats.io/running-a-nats-server/introduction) with JetStream enabled.

```bash
# infra
docker run -d -p 6379:6379 redis/redis-stack-server   # ships with RedisBloom
docker run -d -p 4222:4222 nats -js

# postgres schema
psql -U postgres -c "CREATE DATABASE tupledb;"
psql -U postgres -d tupledb -c "
  CREATE TABLE word_counts (word TEXT PRIMARY KEY, count BIGINT NOT NULL);
  CREATE TABLE processed_events (event_id UUID PRIMARY KEY);
"

# build & run
make build
make run
# or: go run main.go

# kick off a crawl
curl -X POST localhost:8000/run/crawler \
  -H "Content-Type: application/json" \
  -d '{"seed_url": ["https://example.com"]}'

# stop it early
curl "localhost:8000/shutdown/crawler?id=<returned-id>"

# inspect the archived pages
go run reader/read.go
```

---

## What I'd harden for production

Being transparent about the gap between "learning project" and "production-ready":

- [ ] Move the Postgres DSN and other config out of source and into environment variables
- [ ] Add `robots.txt` compliance and configurable per-domain politeness delay (currently a fixed constant, not yet wired into the fetch loop)
- [ ] Replace the fixed 4-way static partitioning with consistent hashing so partition count can change without a full re-crawl
- [ ] Add metrics (Prometheus) for queue depth, dedup rate, flush latency, and DLQ volume
- [ ] Integration tests around the atomic Lua dedup path under real concurrency
- [ ] A supervisory process to replay the DLQ once the root cause of a failed flush is fixed

---

## License

MIT — built for learning, free to learn from.