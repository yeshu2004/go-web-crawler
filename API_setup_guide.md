# Web Crawler + Analysis API - Setup Guide

## Prerequisites
- PostgreSQL running locally
- Redis running on localhost:6379
- Go 1.24.1

## Installation

### 1. PostgreSQL Setup

```bash
# Create database
createdb crawler_db

# Connect and verify
psql -U postgres -d crawler_db
```

### 2. Update Connection Strings

In both `main.go` (crawler) and `api.go` (analysis), update:

```go
connStr := "user=postgres password=YOUR_PASSWORD dbname=crawler_db sslmode=disable"
```

### 3. Install Dependencies

```bash
go mod download
go mod tidy
```

## Running

### Terminal 1 - Start the Crawler

```bash
go run main.go
```

This will:
- Connect to Redis (Bloom Filter)
- Connect to PostgreSQL
- Crawl Wikipedia pages
- Store HTML content in the database

### Terminal 2 - Start the API Server

```bash
go run api.go
```

Server runs on `http://localhost:8080`

## API Endpoints

### 1. Analyze a Specific Page
```bash
curl "http://localhost:8080/api/analyze?url=https://en.wikipedia.org/wiki/Ramayana"
```

**Response:**
```json
{
  "url": "https://en.wikipedia.org/wiki/Ramayana",
  "title": "Ramayana - Wikipedia",
  "description": "...",
  "heading_count": 45,
  "paragraph_count": 120,
  "image_count": 15,
  "internal_links": 450,
  "external_links": 20,
  "word_count": 5430,
  "text_content": "...",
  "heading_titles": ["History", "Characters", ...],
  "crawled_at": "2025-01-15T10:30:00Z"
}
```

### 2. Get All Crawled Pages
```bash
curl "http://localhost:8080/api/pages"
```

**Response:**
```json
{
  "total": 45,
  "pages": [
    {
      "id": 1,
      "url": "https://en.wikipedia.org/wiki/Ramayana",
      "title": "Ramayana - Wikipedia",
      "links_count": 450,
      "crawled_at": "2025-01-15T10:30:00Z"
    }
  ]
}
```

### 3. Search Pages
```bash
curl "http://localhost:8080/api/search?q=Ramayana"
```

### 4. Get Statistics
```bash
curl "http://localhost:8080/api/stats"
```

**Response:**
```json
{
  "total_pages": 45,
  "total_links": 5420,
  "avg_links": 120.44
}
```

## Database Schema

```sql
CREATE TABLE crawled_pages (
    id SERIAL PRIMARY KEY,
    url VARCHAR(2048) UNIQUE NOT NULL,
    html_content TEXT NOT NULL,
    html_hash VARCHAR(64) NOT NULL,
    status_code INT,
    crawled_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    title VARCHAR(255),
    links_count INT
);
```

## Architecture

```
┌─────────────────────────────────────┐
│     Web Crawler (main.go)           │
│  - BFS crawling (8 workers)         │
│  - Redis Bloom Filter               │
│  - Stores HTML in PostgreSQL        │
└────────────┬────────────────────────┘
             │
             ├──────────────┬─────────────────────┐
             ▼              ▼                     ▼
        PostgreSQL      Redis              HTML Files
        - Pages         - Bloom             (in DB)
        - Content       Filter
        - Metadata

┌─────────────────────────────────────┐
│   Analysis API (api.go)             │
│  - /api/analyze                     │
│  - /api/pages                       │
│  - /api/search                      │
│  - /api/stats                       │
└─────────────────────────────────────┘
```

## What Gets Analyzed

- Page title & meta description
- Heading hierarchy (H1-H6)
- Paragraph count
- Image count
- Internal vs external links
- Word count & text content preview
- Crawl timestamp