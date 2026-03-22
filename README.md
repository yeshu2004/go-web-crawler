## Web Crawler

A secure and efficient web crawler written in Go with dual engine support: local crawling and Cloudflare Browser Rendering API integration.

## Features

- **Dual Crawler Engines**: Local multi-threaded crawling or Cloudflare API-based crawling
- **Security Hardened**: SSRF protection, input validation, secure file permissions
- **Bloom Filter Deduplication**: Redis-based duplicate URL prevention
- **Configurable**: Environment-based configuration with secure defaults
- **Graceful Shutdown**: Clean resource management and signal handling
- **Rate Limiting**: Respects politeness delays and API limits

## Security Fixes Applied

- ✅ **Fixed Hardcoded Credentials** (CWE-259, CWE-798)
- ✅ **Fixed SSRF Vulnerability** (CWE-918) with domain allowlisting
- ✅ **Fixed Log Injection** (CWE-117) with input sanitization
- ✅ **Fixed Insecure File Permissions** (CWE-276)
- ✅ **Optimized Redundant Conditionals**


## Setup & Run

### 1. Redis Setup
```bash
# Pull and run Redis Stack
docker pull redis/redis-stack:latest
docker run -d -p 6379:6379 --name redis-stack redis/redis-stack:latest

# Verify Redis is running
docker ps
```

### 2. Environment Configuration
```bash
# Copy example environment file
cp .env.example .env

# Edit .env with your configuration
# For local crawling (default):
CRAWLER_ENGINE=local
REDIS_URL=redis://localhost:6379
ALLOWED_DOMAINS=wikipedia.org,indiatoday.in

# For Cloudflare crawling:
CRAWLER_ENGINE=cloudflare
CLOUDFLARE_API_TOKEN=your_token_here
CLOUDFLARE_ACCOUNT_ID=your_account_id_here
```

### 3. Run the Crawler
```bash
# Local crawler
./test_crawler.sh

# Or directly with Go
go run main.go
```

## Cloudflare Integration

The crawler now supports Cloudflare's Browser Rendering API for enhanced crawling capabilities:

- **Scalable**: Leverages Cloudflare's infrastructure
- **JavaScript Support**: Renders dynamic content
- **Rate Limit Friendly**: Built-in request management
- **Markdown Output**: Clean, structured content extraction

See [CLOUDFLARE_INTEGRATION.md](CLOUDFLARE_INTEGRATION.md) for detailed setup instructions.

## Output

<img width="1280" height="797" alt="Screenshot 2026-02-04 at 10 37 23 AM" src="https://github.com/user-attachments/assets/1cc98cfe-54dd-4031-b37a-cfacfcf688a5" />


<img width="1280" height="800" alt="Screenshot 2026-02-04 at 10 37 57 AM" src="https://github.com/user-attachments/assets/4eab4f4c-13f1-4d37-8d8f-8be1fbfd668a" />

<img width="1017" height="200" alt="Screenshot 2026-02-04 at 10 41 31 AM" src="https://github.com/user-attachments/assets/b72150e3-d7a5-43f8-acb0-a5ddf59f1f68" />

<img width="337" height="469" alt="Screenshot 2026-02-04 at 10 41 01 AM" src="https://github.com/user-attachments/assets/8c93a479-3d67-4d3d-818a-2f082906ceba" />



