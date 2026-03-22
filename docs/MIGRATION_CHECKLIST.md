# Cloudflare Integration Checklist

Complete these steps to integrate Cloudflare Browser Rendering API into your web crawler.

## Phase 1: Preparation

- [ ] **Cloudflare Account Setup**
  - [ ] Have Cloudflare account created
  - [ ] Navigate to Workers AI page
  - [ ] Generate API token with appropriate permissions
  - [ ] Note your Account ID (visible in Workers dashboard)
  
- [ ] **Verify Current Setup**
  - [ ] Redis is running and accessible (`redis-cli ping`)
  - [ ] Current local crawler works (`go run main.go`)
  - [ ] Go version is 1.24.1 or compatible
  
- [ ] **Backup Existing Code**
  - [ ] Commit current code to git: `git commit -am "Pre-refactor backup"`
  - [ ] Create a backup branch: `git checkout -b backup/original-crawler`

## Phase 2: Dependency Updates

- [ ] **Update go.mod**
  - [ ] Copy contents from `go_mod_updated.txt` to `go.mod`
  - [ ] Run `go mod download` to fetch new dependencies
  - [ ] Verify no errors: `go mod tidy`
  
- [ ] **Verify Dependencies Install**
  - [ ] BadgerDB: `go list -m github.com/dgraph-io/badger/v3`
  - [ ] Redis: `go list -m github.com/redis/go-redis/v9`
  - [ ] Run `go build -v` to check compilation

## Phase 3: File Structure Setup

- [ ] **Create Directory Structure**
  ```bash
  mkdir -p cloudflare
  mkdir -p db
  mkdir -p badger_db
  ```

- [ ] **Copy/Create New Files**
  - [ ] Copy `cloudflare_types.go` → `cloudflare/types.go`
  - [ ] Copy `cloudflare_client.go` → `cloudflare/client.go`
  - [ ] Copy `cloudflare_crawler.go` → `cloudflare/crawler.go`
  - [ ] Copy `db_redis.go` → `db/redis.go` (replaces existing)
  - [ ] Copy `db_badger.go` → `db/badger.go`
  - [ ] Rename `main.go` → `main.go.backup`
  - [ ] Copy `main_refactored.go` → `main.go`

- [ ] **Environment Setup**
  - [ ] Copy `.env.example` to `.env`
  - [ ] Update `.env` with your Cloudflare credentials:
    ```
    CLOUDFLARE_API_TOKEN=your_token_here
    CLOUDFLARE_ACCOUNT_ID=your_account_id_here
    ```
  - [ ] Keep `CRAWLER_ENGINE=local` for testing

## Phase 4: Testing Phase

### 4a. Test Local Crawler (Default)

- [ ] **Verify Local Crawler Still Works**
  ```bash
  export CRAWLER_ENGINE=local
  go run main.go
  ```
  Expected behavior: Same as before
  
- [ ] **Verify Redis Bloom Filter**
  - [ ] Check Redis has bloom filter key: `redis-cli --scan --match "wiki_bf*"`
  - [ ] Verify duplicates are skipped in logs

- [ ] **Verify BadgerDB Storage**
  - [ ] Check `./badger_db/` directory exists
  - [ ] Look for `MANIFEST` and `*.sst` files
  
- [ ] **Test Graceful Shutdown**
  - [ ] Start crawler: `go run main.go`
  - [ ] Press Ctrl+C while running
  - [ ] Verify "Shutting down gracefully" message appears

### 4b. Test Cloudflare Crawler

- [ ] **Obtain Cloudflare Credentials**
  - [ ] Get API Token from Cloudflare dashboard
  - [ ] Verify you have Account ID
  - [ ] Test token validity:
    ```bash
    curl -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
      "https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID"
    ```

- [ ] **Set Cloudflare Configuration**
  ```bash
  export CRAWLER_ENGINE=cloudflare
  export CLOUDFLARE_API_TOKEN=your_token
  export CLOUDFLARE_ACCOUNT_ID=your_account_id
  export CLOUDFLARE_POLL_INTERVAL=15
  export CLOUDFLARE_MAX_WAIT_TIME=604800
  ```

- [ ] **Run Cloudflare Crawler**
  ```bash
  go run main.go
  ```
  Expected behavior:
  - Log: "Crawl job initiated with ID: xxx"
  - Log: "Job xxx status: pending/running"
  - Eventually: "Crawl complete: X new pages stored"

- [ ] **Monitor Job Status**
  - [ ] Watch logs for status updates
  - [ ] Verify polling interval matches configuration
  - [ ] Confirm job completion notification

- [ ] **Verify BadgerDB Population**
  - [ ] Check `./badger_db/` has grown
  - [ ] Add debug code to print statistics:
    ```go
    bs, _ := db.NewBadgerStore("./badger_db")
    stats := bs.GetStats()
    fmt.Printf("Pages stored: %d\n", stats["totalPages"])
    ```

- [ ] **Verify Redis Deduplication**
  - [ ] Check Redis has duplicates in logs
  - [ ] Verify count: "X duplicates skipped"

## Phase 5: Integration Testing

- [ ] **Test Crawler Switching**
  ```bash
  # Start with local
  export CRAWLER_ENGINE=local
  go run main.go
  
  # Wait for completion, then switch
  export CRAWLER_ENGINE=cloudflare
  go run main.go
  ```
  Expected: Both complete without errors

- [ ] **Verify Shared State**
  - [ ] Run local crawler first
  - [ ] Switch to Cloudflare
  - [ ] Confirm Bloom Filter deduplication works across both
  - [ ] Same URLs should be skipped in both crawlers

- [ ] **Test Error Conditions**
  - [ ] Run without Redis: `redis-cli SHUTDOWN`
    - Expected: Clear error message about Redis connection
  - [ ] Run with invalid Cloudflare token:
    - Expected: API authentication error
  - [ ] Run with invalid Account ID:
    - Expected: API error response

## Phase 6: Configuration & Tuning

- [ ] **Environment Variables Cleanup**
  - [ ] Remove hardcoded values from code
  - [ ] All config via `.env` or environment
  - [ ] Document all variables in `.env.example`

- [ ] **Logging Levels**
  - [ ] Test with `LOG_LEVEL=debug` (future implementation)
  - [ ] Ensure sensitive data not logged (API tokens)
  - [ ] Verify job IDs and URLs in logs

- [ ] **Performance Tuning**
  - [ ] Local crawler:
    - [ ] Test with `CRAWLER_WORKERS=4` (low)
    - [ ] Test with `CRAWLER_WORKERS=16` (high)
    - [ ] Find optimal politeness: `CRAWLER_POLITENESS_MS=500-2000`
  
  - [ ] Cloudflare crawler:
    - [ ] Test with `CLOUDFLARE_POLL_INTERVAL=10` (fast polling)
    - [ ] Test with `CLOUDFLARE_POLL_INTERVAL=60` (slow polling)

## Phase 7: Production Readiness

- [ ] **Code Review**
  - [ ] Review all new files for:
    - [ ] Proper error handling
    - [ ] Resource cleanup (defer Close())
    - [ ] Context propagation
    - [ ] Logging of important events
  
- [ ] **Documentation**
  - [ ] Update README.md with new crawler options
  - [ ] Add IMPLEMENTATION_GUIDE.md to repo
  - [ ] Document how to switch between crawlers
  - [ ] Add example .env configurations

- [ ] **Security Audit**
  - [ ] Verify API token not in code
  - [ ] Check no credentials in git history: `git log -p | grep -i token`
  - [ ] Ensure .env is in .gitignore
  - [ ] Verify SSL/TLS for API calls

- [ ] **Deployment Preparation**
  - [ ] Document deployment steps
  - [ ] Create initialization scripts
  - [ ] Set up monitoring/alerting (optional)
  - [ ] Prepare rollback plan

- [ ] **Final Testing**
  - [ ] Full end-to-end test with local crawler
  - [ ] Full end-to-end test with Cloudflare crawler
  - [ ] Stress test with many concurrent workers (local)
  - [ ] Long-running crawl test (12+ hours)

## Phase 8: Deployment

- [ ] **Pre-Deployment**
  - [ ] Run full test suite: `go test ./...`
  - [ ] Check for race conditions: `go run -race main.go`
  - [ ] Backup production data (if applicable)

- [ ] **Deployment**
  - [ ] Commit all changes: `git commit -am "Cloudflare integration"`
  - [ ] Create release tag: `git tag v2.0.0-cloudflare`
  - [ ] Push to production
  - [ ] Monitor logs for errors

- [ ] **Post-Deployment**
  - [ ] Verify crawler is running
  - [ ] Check logs for errors
  - [ ] Confirm data storage (BadgerDB)
  - [ ] Monitor resource usage

## Phase 9: Maintenance

- [ ] **Setup Monitoring**
  - [ ] Monitor logs for errors
  - [ ] Track pages crawled per day
  - [ ] Monitor Cloudflare API usage/costs
  - [ ] Track Redis memory usage

- [ ] **Regular Maintenance**
  - [ ] Review error logs weekly
  - [ ] Clean up old BadgerDB data (optional)
  - [ ] Update dependencies monthly
  - [ ] Review Cloudflare costs

- [ ] **Future Enhancements** (Optional)
  - [ ] [ ] Add metrics collection (Prometheus)
  - [ ] [ ] Web dashboard for monitoring
  - [ ] [ ] Support multiple concurrent crawls
  - [ ] [ ] Database-backed job queuing
  - [ ] [ ] Export results to various formats

## Rollback Plan

If you encounter issues:

1. **Keep Backup Branch**
   ```bash
   git checkout backup/original-crawler
   git pull  # Get latest from backup
   ```

2. **Quick Switch**
   ```bash
   # If code is broken:
   git revert <commit-hash>
   
   # If Cloudflare config issues:
   export CRAWLER_ENGINE=local
   ```

3. **Data Recovery**
   - Redis: Data persists, will work with either crawler
   - BadgerDB: Separate for each version, both coexist fine

## Success Criteria

You're done when:

- ✅ Local crawler works exactly as before
- ✅ Cloudflare crawler completes without errors
- ✅ Pages are stored in BadgerDB with compression
- ✅ Deduplication works across both crawlers
- ✅ Environment variables control behavior
- ✅ Graceful shutdown works for both
- ✅ Error conditions are handled gracefully
- ✅ Documentation is complete
- ✅ All code is committed to git

## Support

### Common Issues

**Issue:** `module not found: github/yeshu2004/go-epics/cloudflare`

**Solution:** 
```bash
# Verify file structure
ls -la cloudflare/
# Should contain: types.go, client.go, crawler.go

# Run go mod tidy
go mod tidy
```

**Issue:** `redis: connection refused`

**Solution:**
```bash
# Start Redis
redis-server

# Or in Docker:
docker run -d -p 6379:6379 redis:7-alpine
```

**Issue:** `CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID must be set`

**Solution:**
```bash
# Set environment variables
export CLOUDFLARE_API_TOKEN=your_token_here
export CLOUDFLARE_ACCOUNT_ID=your_account_id_here

# Or update .env file
cat .env  # Verify values are present
```

---

**Last Updated:** 2024-03-23  
**Status:** Ready for Implementation
