#!/bin/bash

# Test script for Cloudflare Crawl API integration

echo "Testing Go Web Crawler with Cloudflare Integration"
echo "=================================================="

# Check if required environment variables are set for Cloudflare
if [ "$CRAWLER_ENGINE" = "cloudflare" ]; then
    if [ -z "$CLOUDFLARE_API_TOKEN" ] || [ -z "$CLOUDFLARE_ACCOUNT_ID" ]; then
        echo "ERROR: CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID must be set for cloudflare engine"
        echo "Please set these environment variables:"
        echo "  export CLOUDFLARE_API_TOKEN='your_token_here'"
        echo "  export CLOUDFLARE_ACCOUNT_ID='your_account_id_here'"
        exit 1
    fi
    echo "✓ Cloudflare credentials found"
fi

# Set default values if not provided
export CRAWLER_ENGINE=${CRAWLER_ENGINE:-"local"}
export REDIS_URL=${REDIS_URL:-"redis://localhost:6379"}
export ALLOWED_DOMAINS=${ALLOWED_DOMAINS:-"wikipedia.org,indiatoday.in"}
export CRAWLER_WORKERS=${CRAWLER_WORKERS:-"4"}
export CRAWLER_POLITENESS_MS=${CRAWLER_POLITENESS_MS:-"1000"}
export MAX_PAGES=${MAX_PAGES:-"10"}

echo "Configuration:"
echo "  Engine: $CRAWLER_ENGINE"
echo "  Redis: $REDIS_URL"
echo "  Allowed Domains: $ALLOWED_DOMAINS"
echo "  Workers: $CRAWLER_WORKERS"
echo "  Max Pages: $MAX_PAGES"
echo ""

# Check if Redis is running
echo "Checking Redis connection..."
if command -v redis-cli &> /dev/null; then
    if redis-cli ping &> /dev/null; then
        echo "✓ Redis is running"
    else
        echo "⚠ Redis is not responding. Make sure Redis is running:"
        echo "  docker run -d -p 6379:6379 --name redis-stack redis/redis-stack:latest"
    fi
else
    echo "⚠ redis-cli not found. Assuming Redis is running..."
fi

echo ""
echo "Starting crawler with $CRAWLER_ENGINE engine..."
echo "Press Ctrl+C to stop"
echo ""

# Run the crawler
go run main.go