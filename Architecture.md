# Architecture — go-web-crawler

## 1. System Overview

The system is a distributed, event-driven web crawler and word-frequency indexer. It is split into two independently deployable processes that communicate through NATS JetStream:

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                          CRAWLER PROCESS  (main.go)                          │
│                                                                              │
│  Seed URLs ──► URL Queue (chan, cap 10k)                                     │
│                       │                                                      │
│              ┌────────▼────────┐                                             │
│              │  8 × Worker     │  goroutines                                 │
│              │  Goroutines     │                                             │
│              └──┬──────────┬──┘                                             │
│                 │          │                                                 │
│     ┌───────────▼──┐   ┌───▼─────────────────────┐                         │
│     │  BadgerDB    │   │  Redis Stack (BF + NATS) │                         │
│     │  (gzip HTML) │   │  Bloom Filter  +  Publish│                         │
│     └──────────────┘   └──────────────────────────┘                         │
└──────────────────────────────────────────────────────────────────────────────┘
                                        │ NATS JetStream
                                        │ TUPLE.0 … TUPLE.3
                                        ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                        CONSUMER PROCESS  (consumer/main.go)                  │
│                                                                              │
│        ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│        │Worker-0  │  │Worker-1  │  │Worker-2  │  │Worker-3  │              │
│        │TUPLE.0   │  │TUPLE.1   │  │TUPLE.2   │  │TUPLE.3   │              │
│        └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘              │
│             │             │             │              │                     │
│             └─────────────┴─────────────┴──────────────┘                    │
│                                    │                                         │
│                         ┌──────────▼──────────┐                             │
│                         │    PostgreSQL        │                             │
│                         │  word_counts table   │                             │
│                         └─────────────────────┘                             │
└──────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Component Breakdown

### 2.1 Crawler (`main.go`)

**Responsibility:** Discover URLs, fetch pages, store raw content, deduplicate, and publish word-frequency events.

#### URL Queue
- A Go buffered channel (`chan string`, capacity 10,000) acts as the crawl frontier.
- Seed URLs are pushed at startup; discovered links are re-enqueued by workers.
- When the channel is full, new links are silently dropped.

#### Worker Pool
- 8 goroutines run in parallel, each blocking on the channel.
- Each worker sleeps for a configurable politeness delay (800 ms) before each fetch — providing basic rate-limiting per worker.
- Workers are shut down by cancelling the shared `context.Context`.

#### Fetch & Parse (`fetchBody`, `extractLinks`, `extractText`)
- `net/http.Client` with a 30-second timeout performs HTTP GET.
- `golang.org/x/net/html` tokenises the HTML response for both link extraction and visible-text extraction.
- Relative URLs are resolved against the page's base URL; cross-domain links and non-HTTP schemes are dropped.
- Query strings and URL fragments are stripped during normalisation to improve deduplication hit rates.

#### Bloom Filter (`db/reddis.go`)
- Redis Stack's `BF.RESERVE` / `BF.ADD` / `BF.EXISTS` commands implement a probabilistic seen-URL set.
- Capacity: 10,000,000 entries. False-positive rate: 0.1%.
- The URL is first SHA-256 hashed before being inserted, keeping the filter key uniform in length.
- **Trade-off:** Uses ~7 MB of Redis memory (vs. hundreds of MB for a naive hash-set at this scale), at the cost of a small chance of falsely skipping an unseen URL.

#### HTML Storage (`BadgerDB + compress/gzip.go`)
- The raw HTML body is gzip-compressed (stdlib `compress/gzip`) before storage.
- BadgerDB operates in LSM-only mode (`LSMOnlyOptions`), optimised for write-heavy sequential workloads — ideal for bulk crawling where pages are written once and rarely read back.
- The SHA-256 URL hash is used as the BadgerDB key, giving O(1) point lookups.

#### Word Frequency & Partitioned Publishing (`publishTuple`, `partitionFor`)
- `buildFreqMap` lowercases all text, splits on non-alphanumeric characters, discards tokens shorter than 3 characters, and counts occurrences.
- Each `(word, count, urlHash)` triple is serialised as JSON and published to NATS JetStream on subject `TUPLE.<partitionID>`.
- Partition assignment: `SHA-256(word) % 4`. This is deterministic — the same word always goes to the same consumer partition — enabling per-partition in-memory aggregation without locks or cross-worker coordination.

---

### 2.2 NATS JetStream (`nats/server.go`)

**Responsibility:** Durable, ordered, at-least-once message delivery between the crawler and the consumers.

#### Stream Configuration
| Setting | Value | Rationale |
|---------|-------|-----------|
| Name | `TUPLE` | Logical grouping for all word-frequency events |
| Subjects | `TUPLE.*` | Wildcard matches all 4 partition subjects |
| Storage | `FileStorage` | Survives NATS server restarts |
| Retention | `LimitsPolicy` | Messages expire by size/age, not after consumption |
| Replicas | `1` | Single-node deployment; increase for HA |

#### Why NATS JetStream over Kafka?
- Zero-infrastructure for small deployments (single binary, no ZooKeeper/KRaft).
- Built-in subject-based partitioning without needing a separate topic-partition mapping layer.
- Sub-millisecond publish latency at this scale.
- The JetStream `Fetch` API supports micro-batching natively.

---

### 2.3 Consumer (`consumer/main.go` + `nats/server.go`)

**Responsibility:** Aggregate word-frequency tuples and persist them to PostgreSQL.

#### Partitioned Consumer Goroutines
- 4 goroutines each own one NATS subject (`TUPLE.0` through `TUPLE.3`).
- Each has a **durable, named consumer** (`tuple-indexer-0` etc.) so it can resume from the last acknowledged message after a crash or restart.
- Messages are fetched in micro-batches of 10 with a 2-second max-wait (`consumer.Fetch`).

#### In-Memory Aggregation Buffer
- Each partition worker maintains a `map[string]int` (pre-allocated at `batchSize = 1000`).
- Incoming tuples are merged into the map: `tupleBatch[word] += count`.
- This deduplication-before-flush step means the DB sees far fewer, larger writes.

#### Batch Flush (`flushDB`)
- When the in-memory count reaches 1,000 or on graceful shutdown, the entire batch is flushed in a single PostgreSQL transaction.
- Upsert query: `INSERT … ON CONFLICT (word) DO UPDATE SET count = word_counts.count + EXCLUDED.count` — atomically increments existing counts.
- Messages are only `Ack()`-ed after a successful flush, ensuring at-least-once delivery.

---

### 2.4 PostgreSQL (`db/postgres.go`)

**Responsibility:** Persistent global word-frequency store.

#### Schema
```sql
CREATE TABLE word_counts (
  id         SERIAL,
  word       TEXT    NOT NULL PRIMARY KEY,
  count      BIGINT  NOT NULL,
  updated_at TIMESTAMP DEFAULT NOW()
);
```

#### Connection Pool
- `pgx v5` with `MaxOpenConns=20`, `MaxIdleConns=10`, `ConnMaxLifetime=5m`.
- The pool is shared across all 4 consumer goroutines inside the same process.

#### Why PostgreSQL over a NoSQL store?
- Native `ON CONFLICT DO UPDATE` (upsert) makes atomic counter increments trivial.
- Future full-text search indexing (`tsvector`, GIN indexes) can be built directly on this table without data migration.
- Familiar tooling and strong consistency guarantees.

---

## 3. Data Flow (End-to-End)

```
1. Seed URL enters queue channel
         │
         ▼
2. Worker fetches HTTP response (30s timeout)
         │
         ▼
3. extractText() → buildFreqMap()
         │
         ├──► publishTuple() ──► NATS JetStream (TUPLE.<partition>)
         │
         ├──► GzipCompress() ──► BadgerDB.Update() (key = sha256(url))
         │
         └──► extractLinks() ──► for each link:
                                    BF.ADD(sha256(link))
                                    if added → queue <- link

4. Consumer goroutine Fetch() from NATS
         │
         ▼
5. processTuple() → tupleBatch[word] += count
         │
         ▼ (when count >= 1000 OR ctx.Done())
6. flushDB() → PostgreSQL transaction
              INSERT … ON CONFLICT DO UPDATE
```

---

## 4. Concurrency Model

```
main goroutine
├── signal goroutine (os.Interrupt → cancel())
└── 8 × worker goroutine  [share: queue chan, *badger.DB, *redis.Client, *nats.Client]
    each worker:
    ├── fetchBody()
    ├── buildFreqMap() + publishTuple()   → NATS publish (thread-safe)
    ├── badgerDB.Update()                  → BadgerDB is goroutine-safe
    └── BF.ADD() + queue <- link           → Redis ops are goroutine-safe

consumer main goroutine
├── signal goroutine (os.Interrupt → cancel())
└── 4 × consumer goroutine  [each owns its own tupleBatch map — no sharing]
    each consumer:
    ├── consumer.Fetch(10, ...)
    ├── processTuple() → tupleBatch (local map, no lock needed)
    └── flushDB()      → shared *sql.DB pool (connection-level serialisation)
```

**Shared state analysis:**
- The URL queue channel is safe for concurrent send/receive.
- `duplicateCount` uses `sync/atomic` — no lock needed.
- BadgerDB, Redis, and NATS clients are goroutine-safe by their respective library designs.
- Each consumer partition owns its own `tupleBatch` map exclusively — no cross-partition sharing.
- The PostgreSQL connection pool serialises DB access at the connection level.

---

## 5. Deployment Topology

```
┌──────────────┐     localhost:4222      ┌──────────────┐
│  go run      │ ──── NATS JetStream ───► │  go run      │
│  main.go     │                         │  consumer/   │
│  (Crawler)   │                         │  main.go     │
└──────┬───────┘                         └──────┬───────┘
       │                                        │
       │  localhost:6379                        │  localhost:5432
       ▼                                        ▼
┌──────────────┐                        ┌──────────────┐
│  Redis Stack │                        │  PostgreSQL  │
│  (Docker)    │                        │  (Docker)    │
└──────────────┘                        └──────────────┘
       │
  (embedded)
       │
┌──────────────┐
│  BadgerDB    │
│  ./crwal_db  │
└──────────────┘
```

All components run on localhost in the current setup. For production:
- Replace the NATS single-node with a 3-node JetStream cluster.
- Move BadgerDB to a replicated object store or switch to a distributed KV.
- Containerise both Go binaries and use environment-based config injection.
- Add a PostgreSQL read-replica for analytics queries without impacting write throughput.

---

