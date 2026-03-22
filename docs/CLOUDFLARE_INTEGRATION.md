# Cloudflare Crawl API Integration

This project now supports both local crawling and Cloudflare's Browser Rendering API for web crawling.

## Security Fixes Applied

1. **Fixed Hardcoded Credentials**: All sensitive data now uses environment variables
2. **Fixed SSRF Vulnerability**: Added URL validation and domain allowlisting
3. **Fixed Log Injection**: Sanitized user input before logging
4. **Fixed File Permissions**: Changed from 0644 to 0600 for secure file access
5. **Fixed Redundant Conditionals**: Optimized conditional logic

## Cloudflare Setup

1. **Get API Credentials**:
   - Cloudflare API Token with Browser Rendering permissions
   - Account ID from Cloudflare dashboard

2. **Set Environment Variables**:
   ```bash
   export CLOUDFLARE_API_TOKEN="your_token_here"
   export CLOUDFLARE_ACCOUNT_ID="your_account_id"
   export CRAWLER_ENGINE="cloudflare"
   ```

3. **Optional Configuration**:
   ```bash
   export CLOUDFLARE_POLL_INTERVAL="15"  # seconds
   export CLOUDFLARE_MAX_WAIT_TIME="604800"  # 7 days in seconds
   export ALLOWED_DOMAINS="wikipedia.org,example.com"
   export MAX_PAGES="1000"
   ```

## Usage

### Local Crawler (Default)
```bash
# Uses local worker pool approach
CRAWLER_ENGINE=local go run main.go
```

### Cloudflare Crawler
```bash
# Uses Cloudflare Browser Rendering API
CRAWLER_ENGINE=cloudflare \
CLOUDFLARE_API_TOKEN=your_token \
CLOUDFLARE_ACCOUNT_ID=your_account_id \
go run main.go
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CRAWLER_ENGINE` | `local` | `local` or `cloudflare` |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection string |
| `CLOUDFLARE_API_TOKEN` | - | Required for cloudflare engine |
| `CLOUDFLARE_ACCOUNT_ID` | - | Required for cloudflare engine |
| `ALLOWED_DOMAINS` | `wikipedia.org,indiatoday.in` | Comma-separated allowed domains |
| `CRAWLER_WORKERS` | `8` | Number of local workers |
| `CRAWLER_POLITENESS_MS` | `800` | Delay between requests (ms) |
| `MAX_PAGES` | `1000` | Maximum pages to crawl |

## Features

- **Dual Engine Support**: Switch between local and Cloudflare crawling
- **Security Hardened**: SSRF protection, input validation, secure file permissions
- **Bloom Filter Deduplication**: Prevents crawling duplicate URLs
- **Graceful Shutdown**: Handles interrupts cleanly
- **Configurable**: Environment-based configuration
- **Rate Limiting**: Respects politeness delays and API limits