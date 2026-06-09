## Go-web-crawler
 
A distributed, event-driven web crawler and word-frequency indexer built in Go. The system crawls web pages concurrently, extracts and compresses HTML content into a local key-value store, and pipelines word-frequency tuples through a message broker into a relational database — forming the foundation of a search-engine-style inverted index.

## Overview
 
The crawler operates as two independently runnable binaries:
 
1. **Crawler** (`main.go`) — Seeds URLs, fetches pages concurrently, stores compressed HTML in BadgerDB, deduplicates links via a Redis Bloom Filter, and publishes per-page word-frequency tuples onto a NATS JetStream.
2. **Consumer** (`consumer/main.go`) — Runs partitioned workers that subscribe to NATS JetStream subjects, accumulate word-count tuples in memory, and batch-flush them into PostgreSQL using an upsert strategy

## Features
 
- **Concurrent crawling** — 8 parallel goroutine workers drain a shared URL queue (capacity 10,000).
- **Bloom Filter deduplication** — Redis Stack's native `BF.ADD` / `BF.EXISTS` commands prevent revisiting already-seen URLs with a configurable false-positive rate (default 0.1%).
- **Compressed HTML storage** — Raw page bodies are gzip-compressed before being written to BadgerDB (LSM-only mode), reducing on-disk footprint significantly.
- **Message-driven word indexing** — Word-frequency tuples are published to NATS JetStream, partitioned deterministically by a SHA-256 hash of the word, so the same word always routes to the same consumer partition — enabling safe in-memory aggregation without cross-partition contention.
- **Batch DB writes** — Each consumer accumulates 1,000 tuples in memory before issuing a single transactional upsert to PostgreSQL, dramatically reducing write amplification.
- **Graceful shutdown** — Both processes respond to `SIGINT` (`Ctrl+C`), flush in-flight data, and exit cleanly.
- **Domain-scoped crawling** — The URL resolver enforces same-origin crawling, normalises paths, and strips query strings and fragments.


## PostgresSQL Setup Docker

```bash
  docker run --name my-postgres -e POSTGRES_PASSWORD=yeshu2004 -p 5432:5432 -d postgres
```

```bash
  docker exec -it my-postgres psql -U postgres
```

```bash
  CREATE DATABASE tupledb;
```

```bash
  \l
```

```bash
  \c tupledb
```

```bash
  CREATE TABLE word_counts (
    id SERIAL,
    word TEXT NOT NULL PRIMARY KEY,
    count BIGINT NOT NULL,
    updated_at TIMESTAMP DEFAULT NOW()
  );
```

```bash
  DROP TABLE word_counts
```

## Set Up Redis Stack with Docker\*\*:

     ```bash
     docker pull redis/redis-stack:latest
     ```
     ```bash
     docker run -d -p 6379:6379 --name redis-stack redis/redis-stack:latest
     ```
     ```bash
     docker ps
     ```

## Run

- To run the web crawler (in root dir)

```bash
  go run main.go
```

- To run consumer

```bash
  go run consumer/main.go
```

## Limitations

Crawler is currently:

- Ignoring all cross-domain links i.e right now focused crawler (mutiple-single domain)
- Only crawling same-domain pages
- Silently drops links when queue is full