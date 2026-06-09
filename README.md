## Web Crawler

A simple and efficient web crawler written in Go. This is designed for crawling web pages and following links to deepen exploration(BFS approch).

## Features

- Multi-threaded crawling for efficiency
- Bloom Filter for Duplicates URL
- Customizable depth and URL filtering
- Graceful handling of robots.txt
- Parsing HTML and extraction of links
- Added comments for easy work flow

## Limitations

Crawler is currently:

- Ignoring all cross-domain links i.e right now focused crawler (mutiple-single domain)
- Only crawling same-domain pages
- Silently drops links when queue is full

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
