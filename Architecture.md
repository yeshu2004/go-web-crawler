# Architecture — go-web-crawler

## 1. System Overview

The system is a single Go binary exposing an HTTP API that spins up **isolated, per-request crawl instances**, plus a background pool of NATS JetStream consumers running as goroutines inside that same process. Crawling and word-count aggregation are decoupled through a partitioned NATS stream, but — unlike a two-service design — both sides currently run in the same `main.go` process, started together at boot.

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│                                 SINGLE BINARY (main.go)                           │
│                                                                                   │
│   HTTP API                                    Consumer goroutines                 │
│   ─────────                                   ────────────────────                │
│   POST /run/crawler        ┌───────────┐      go consumer.Consume(ctx)            │
│   GET  /shutdown/crawler ─►│  Crawler   │            │                            │
│   GET  /                   │  Manager   │      4 × partition consumer goroutines  │
│                             │ (id→cancel)│      (TUPLE.0 … TUPLE.3)               │
│                             └─────┬──────┘            │                           │
│                                   │                    │                          │
│              each request spins  ▼                    │                           │
│              up an isolated Crawler{}                 │                           │
│              (own UUID, own Bloom-filter key,          │                          │
│               own Redis queue key) — infra is           │                         │
│               shared, crawl state is not                 │                        │
└───────────────────────────────────────────────────────────────────────────────────┘
        │                    │                                    │
        ▼                    ▼                                    ▼
 ┌─────────────┐     ┌──────────────────┐                ┌─────────────────┐
 │  BadgerDB   │     │  Redis Stack       │                │  NATS JetStream   │
 │  (gzip HTML,│     │  Bloom filter +     │  publish ──►  │  TUPLE.0 … TUPLE.3 │
 │  ./crwal_db)│     │  frontier queue      │                │  + DLQ stream       │
 └─────────────┘     │  (atomic Lua script)  │                └─────────┬─────────┘
                      └──────────────────────┘                          │
                                                                          ▼
                                                                ┌──────────────────┐
                                                                │   PostgreSQL       │
                                                                │  word_counts +      │
                                                                │  processed_events    │
                                                                └──────────────────┘
```

---

## 2. Component Breakdown

### 2.1 HTTP API & Crawler Manager (`router/router.go`, `router/manager.go`)

**Responsibility:** Accept crawl requests, run each crawl as an isolated, cancellable unit of work.

- `POST /run/crawler` takes `{"seed_url": [...]}`, creates a new `Crawler` (see 2.2), registers its `context.CancelFunc` in a thread-safe `CrawlerManager` keyed by the crawler's UUID, and returns `202 Accepted` with the crawl `id` immediately — the crawl itself runs in a background goroutine.
- `GET /shutdown/crawler?id=...` looks up that ID in the manager and calls its `cancel()`, stopping just that crawl's workers without affecting any other concurrently running crawl.
- `GET /` is a liveness check.
- `ConnectInfra()` wires up the shared Redis client, BadgerDB handle, and NATS/Postgres client once at startup; these are shared across every crawl, while each `Crawler` gets its own Bloom-filter key and queue key.
- All routes are wrapped by `middleware.SrvMiddleware`, which sets permissive CORS headers and logs method/path/remote-addr for every request.

**Multi-tenancy note:** unlike a single global crawl, this design lets multiple independent crawls run concurrently without their "seen URL" state or frontiers colliding — each `Crawler{}` is scoped by its own `crawler:<id>:bloom` and `queue:<id>` keys in Redis.

---

### 2.2 Crawler (`crawler/crawl.go`)

**Responsibility:** Discover URLs, fetch pages, store raw content, deduplicate, and publish word-frequency events — for one isolated crawl instance.

#### Frontier: Redis, not a Go channel
The frontier is a **Redis list**, not an in-memory buffered channel. Workers pop the next URL with `BRPOP` (5-second blocking wait) instead of receiving from a channel.

> This was a deliberate fix, not the original design: an earlier version used a buffered `chan string` (cap 10,000) as the queue. Under load, all 8 workers ended up blocked *sending* new links back into the full channel, with none left free to *receive* and drain it — a self-inflicted deadlock. Moving the frontier to Redis removes the in-memory capacity ceiling entirely; workers block on `BRPOP` against a store, not against each other.

#### Atomic dedup + enqueue (single Lua script)
Checking "have we seen this URL?" and "add it to the queue" happen as **one atomic Redis Lua script**, not two separate round trips — eliminating the race where two workers both see a URL as new and enqueue it twice:
```lua
if redis.call("BF.EXISTS", KEYS[1], ARGV[1]) == 0 then
    redis.call("BF.ADD", KEYS[1], ARGV[1])
    redis.call("LPUSH", KEYS[2], ARGV[2])
    return 1
end
return 0
```
- Bloom filter capacity: 10,000,000 entries, false-positive rate 0.1% (`BF.RESERVE` via `InitializeBloomFilterTest`).
- URLs are SHA-256 hashed before insertion, keeping filter keys uniform in length and the raw URL out of the filter itself.

#### Worker Pool
- 8 goroutines per crawl instance (`workers = 8`), each looping until `ctx.Done()`.
- A `politeness = 800ms` constant is defined but **not currently wired into the fetch loop** — there's no per-request delay applied today. Flagged under "known gaps" below.
- `duplicateCount` (an `atomic.Int64`) tracks skipped-as-already-seen links and logs a sample (first 100, then every 5,000th) rather than logging every duplicate.

#### Fetch & Parse (`fetchBody`, `extractLinks`, `extractText`)
- `net/http.Client` with a 30-second timeout.
- `golang.org/x/net/html` walks the parsed DOM once for text extraction (skipping `<script>`/`<style>`) and once for `<a href>` link extraction.
- Links are resolved against the page's base URL; cross-origin links, non-HTTP(S) schemes, and URL fragments/query strings are dropped during normalisation to improve dedup hit rate.

#### HTML Storage (`BadgerDB` + `compress/gzip.go`)
- Every fetched page body is gzip-compressed and written to BadgerDB, opened in `LSMOnlyOptions` mode — tuned for the write-heavy, rarely-read-back access pattern of archiving crawled pages.
- Keyed by `sha256(url)` for O(1) point lookups. A standalone tool (`reader/read.go`) can decompress and dump every stored page back to `.html` files for inspection.

#### Word Frequency & Partitioned Publishing (`publishTupleWithRetry`, `partitionFor`)
- `buildFreqMap` lowercases text, splits on non-alphanumeric runs, discards tokens under 3 characters, and counts occurrences.
- Words are **bucketed by partition first**, then published as one **batched JSON array per partition per page** (not one NATS message per word) — `SHA-256(word) % 4` decides the partition, so a given word is always routed to the same partition/consumer.
- Publishing retries up to 3 times with exponential backoff (`publishTupleWithRetry`) before giving up on a batch for that page.

---

### 2.3 NATS JetStream (`nats/server.go`)

**Responsibility:** Durable, at-least-once delivery of word-tuple batches from crawlers to consumers, plus a dead-letter path for batches that repeatedly fail to persist.

#### Stream Configuration
| Setting | Value | Rationale |
|---|---|---|
| Name | `TUPLE` | Logical grouping for all word-frequency batches |
| Subjects | `TUPLE.*` (`TUPLE.0`–`TUPLE.3`) | 4 static partitions, one subject per consumer |
| Storage | `FileStorage` | Survives NATS server restarts |
| Retention | `LimitsPolicy` | Messages expire by size/age, not on consumption |
| Replicas | `1` | Single-node deployment; increase for HA |

#### Dead Letter Queue
A second stream, `DLQ` (subject `DLQ.>`), exists specifically for batches that fail to flush to Postgres after retries. This keeps a slow or broken DB from silently losing data or wedging a consumer's fetch loop indefinitely — failed batches are pushed to `DLQ.tuple-event` (itself retried up to 3 times), and only then are the original messages ACKed off the `TUPLE` stream.

#### Why NATS JetStream over Kafka?
- Single binary, no ZooKeeper/KRaft — minimal ops overhead for this scale.
- Subject-based partitioning without a separate topic/partition abstraction.
- The `Fetch()` API gives natural micro-batching for free.

---

### 2.4 Consumers (`consumer/consume.go` + `nats/server.go`)

**Responsibility:** Durably persist word-frequency tuples to PostgreSQL, exactly-once in effect, despite at-least-once NATS delivery.

**Run mode:** `consumer.Consume(ctx)` is launched as a goroutine from `main.go` (`go consumer.Consume(ctx)`) — it runs **in the same process** as the HTTP API, not as a separately deployed binary. It creates its own NATS/Postgres client, ensures the `TUPLE` stream exists, and fans out into 4 partition-owning goroutines via a `sync.WaitGroup`.

#### Partitioned consumer goroutines
- 4 goroutines, each bound to one durable, named consumer (`tuple-indexer-0` … `tuple-indexer-3`) filtered to its own subject — so each resumes from its last ACKed position after a crash or restart, and partitions never cross-process each other's messages.
- Messages are pulled in micro-batches (`Fetch(10, FetchMaxWait(1s))`).

#### Batching: size- *and* time-based, no in-memory aggregation
Each consumer accumulates **raw events**, not a pre-aggregated `map[string]int`:
- Flush triggers when *any* of: 1,000 messages pulled (`batchSize`), 5,000 individual word events accumulated (`maxBatchEvents`), or 5 seconds have elapsed since the batch started (`flushInterval`) — whichever comes first.
- Deduplication and counting both happen **in the database**, not in a local map — see below.

#### Idempotent flush (`flushDB`)
Every event carries a stable UUID assigned at crawl time. The flush is a single Postgres transaction per batch:
```sql
INSERT INTO processed_events(event_id) VALUES ($1)
ON CONFLICT (event_id) DO NOTHING RETURNING event_id;
-- only for rows that were newly inserted:
INSERT INTO word_counts(word, count) VALUES ($1, $2)
ON CONFLICT (word) DO UPDATE SET count = word_counts.count + EXCLUDED.count;
```
Because the "have I processed this event before?" check and the count increment happen in the same transaction, a JetStream redelivery of an already-committed message becomes a safe no-op — the system achieves **effectively-once aggregation on top of at-least-once delivery**, without needing exactly-once messaging semantics from NATS itself.

Messages are only `Ack()`-ed after a successful flush; a failed flush is retried (`flushMaxRetry = 3`) before being diverted to the DLQ.

---

### 2.5 PostgreSQL (`db/postgres.go`)

**Responsibility:** Persistent, idempotent global word-frequency store.

#### Schema (as actually used by `flushDB`)
```sql
CREATE TABLE word_counts (
  word  TEXT    PRIMARY KEY,
  count BIGINT  NOT NULL
);

CREATE TABLE processed_events (
  event_id UUID PRIMARY KEY
);
```
`processed_events` is the idempotency ledger described above — every event ID that has ever been successfully counted lives here, so redelivered messages are recognised and skipped rather than double-counted.

#### Connection Pool
- `pgx v5` (via `database/sql` + `pgx/v5/stdlib`), `MaxOpenConns=20`, `MaxIdleConns=10`, `ConnMaxLifetime=5m`.
- One pool, shared across all 4 consumer goroutines in the process.

#### Why PostgreSQL over a NoSQL store?
- Native `ON CONFLICT DO UPDATE` makes atomic counter increments trivial and transactional.
- The `processed_events` idempotency pattern relies on transactional guarantees that are awkward to get right in most NoSQL stores.
- Future full-text indexing (`tsvector`/GIN) can be layered on `word_counts` without a data migration.

---

## 3. Data Flow (End-to-End)

```
1. POST /run/crawler {"seed_url": [...]}
        │
        ▼
2. CrawlerManager creates isolated Crawler{id, bfKey, queueKey}
   seeds enqueued via atomic Lua(BF.EXISTS + BF.ADD + LPUSH)
        │
        ▼
3. 8 workers BRPOP url from Redis frontier
        │
        ▼
4. fetchBody() (30s timeout) → extractText() → buildFreqMap()
        │
        ├──► bucket words by SHA-256(word) % 4
        │      └──► publishTupleWithRetry() → NATS JetStream (TUPLE.<partition>, batched JSON)
        │
        ├──► GzipCompress(body) → BadgerDB.Update(key = sha256(url))
        │
        └──► extractLinks() → for each link:
                                 atomic Lua(BF.EXISTS + BF.ADD + LPUSH)
                                 if not new → duplicateCount++

5. Consumer goroutine (per partition) Fetch(10, 1s) from its subject
        │
        ▼
6. processTuple() unmarshals batch → appended to pendingEvents (raw, unaggregated)
        │
        ▼ (flush when: 1000 msgs, OR 5000 events, OR 5s elapsed)
7. flushDB() — one Postgres transaction:
       INSERT processed_events (skip if event_id already seen)
       INSERT/UPDATE word_counts (ON CONFLICT DO UPDATE count += count)
        │
        ├─ success → Ack() every message in the batch
        └─ failure after 3 retries → push batch to DLQ.tuple-event → Ack() to unblock partition
```

---

## 4. Concurrency Model

```
main goroutine
├── HTTP server (net/http, CORS + logging middleware)
├── go consumer.Consume(ctx)
│     └── 4 × partition consumer goroutine  [each owns its own pendingEvents slice — no sharing]
│           each consumer:
│           ├── consumer.Fetch(10, 1s)
│           ├── processTuple() → local pendingEvents (no lock needed)
│           └── flushDB()      → shared *sql.DB pool (connection-level serialisation)
│
└── per HTTP request to /run/crawler:
      new Crawler{} registered in CrawlerManager (mutex-protected map)
      └── 8 × worker goroutine  [share: c.rdb, c.badger, c.nats — all goroutine-safe by design;
                                  own: c.queueKey, c.bfKey — isolated per crawl]
            each worker:
            ├── BRPOP from Redis frontier (this crawl's queueKey)
            ├── fetchBody() + buildFreqMap() + publishTupleWithRetry()
            ├── badger.Update()          → BadgerDB is goroutine-safe
            └── atomic Lua enqueue        → Redis ops are goroutine-safe
```

**Shared state analysis:**
- `CrawlerManager.running` (map of id → cancel func) is protected by a `sync.Mutex`.
- `duplicateCount` uses `sync/atomic` — no lock needed.
- The Redis frontier and Bloom filter are per-crawl-instance keys, so concurrent crawls never share queue or dedup state — only the underlying Redis/BadgerDB/NATS *connections* are shared.
- Each consumer partition owns its own `pendingEvents`/`pendingMsgs` slices exclusively; cross-partition state never needs to be shared because partitioning is deterministic by word hash.
- The PostgreSQL connection pool serialises DB access at the connection level; correctness under concurrent flushes comes from the `processed_events` idempotency table, not from external locking.

---

## 5. Deployment Topology

```
                     single binary: bin/storage  (go build -o bin/storage)
                                   │
                     ┌─────────────┴─────────────┐
                     │   HTTP :8000                 │
                     │   + 4 consumer goroutines       │
                     └───┬──────────┬───────────┬──────┘
                         │          │            │
             localhost:6379   localhost:4222   localhost:5432
                         │          │            │
                         ▼          ▼            ▼
                 ┌─────────────┐ ┌────────┐ ┌────────────┐
                 │ Redis Stack  │ │  NATS   │ │ PostgreSQL   │
                 │ (Bloom + queue)│ (JetStream)│ (word_counts, │
                 └─────────────┘ └────────┘ │  processed_events)│
                         │                    └────────────┘
                   (embedded, local)
                         │
                 ┌─────────────┐
                 │  BadgerDB    │
                 │  ./crwal_db  │
                 └─────────────┘
```

All components run on localhost in the current setup, and the crawler + consumers share one OS process. For production:

- Split the consumer goroutines back out into their own deployable binary (the code is already structured for this — `consumer.Consume(ctx)` has no dependency on the HTTP layer) so crawl throughput and indexing throughput can scale independently.
- Replace the single-node NATS JetStream with a 3-node cluster.
- Move BadgerDB to a replicated object store, or shard it per crawl instance if archived pages need to survive a single-node loss.
- Containerise the binary and move connection strings (currently hardcoded `localhost` addresses and a plaintext Postgres password in `db/postgres.go`) to environment-based config.
- Add a PostgreSQL read replica for analytics queries without impacting write throughput.
- Wire up the currently-unused `politeness` constant as an actual per-domain rate limit, and add `robots.txt` compliance before crawling anything beyond trusted seeds.

---

- The crawler and consumer are **not** two separately deployed processes today — both start from `main.go` in one binary. The two-process diagram was the target design; the code hasn't split them apart yet.
