package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	db "github/yeshu2004/go-epics/db"
	cfcrawler "github/yeshu2004/go-epics/cloudflare"
	"golang.org/x/net/html"
)

// Crawler interface defines the contract for different crawler implementations.
type Crawler interface {
	Crawl(ctx context.Context, urls []string) error
	Close() error
}

// LocalCrawler implements the Crawler interface using the original worker pool approach.
type LocalCrawler struct {
	workers     int
	politeness  time.Duration
	queue       chan string
	wg          sync.WaitGroup
	client      *http.Client
	redisDB     *redis.Client
	bloomKey    string
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewLocalCrawler creates a new local crawler instance.
func NewLocalCrawler(workers int, politeness time.Duration, redisClient *redis.Client, bloomKey string) *LocalCrawler {
	ctx, cancel := context.WithCancel(context.Background())
	return &LocalCrawler{
		workers:    workers,
		politeness: politeness,
		queue:      make(chan string, 10000),
		client:     &http.Client{Timeout: 30 * time.Second},
		redisDB:    redisClient,
		bloomKey:   bloomKey,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Crawl starts the local crawling process.
func (lc *LocalCrawler) Crawl(ctx context.Context, urls []string) error {
	// Seed initial URLs
	for _, u := range urls {
		if !db.SeenBefore(ctx, lc.redisDB, lc.bloomKey, u) {
			db.MarkSeen(ctx, lc.redisDB, lc.bloomKey, u)
			select {
			case lc.queue <- u:
			default:
				log.Printf("Queue full, skipping seed URL: %s", u)
			}
		}
	}

	// Start worker goroutines
	for i := 0; i < lc.workers; i++ {
		lc.wg.Add(1)
		go lc.worker()
	}

	// Wait for all workers to complete
	lc.wg.Wait()
	log.Println("Local crawl completed successfully!")
	return nil
}

// worker processes URLs from the queue.
func (lc *LocalCrawler) worker() {
	defer lc.wg.Done()

	for {
		select {
		case <-lc.ctx.Done():
			return
		case u, ok := <-lc.queue:
			if !ok {
				log.Println("Worker exiting: queue closed")
				return
			}

			time.Sleep(lc.politeness)

			body, err := lc.fetchBody(u)
			if err != nil {
				log.Printf("Failed to fetch %s: %v", u, err)
				continue
			}

			links := lc.extractLinks(body, u)

			for _, link := range links {
				if db.SeenBefore(lc.ctx, lc.redisDB, lc.bloomKey, link) {
					continue
				}
				db.MarkSeen(lc.ctx, lc.redisDB, lc.bloomKey, link)

				select {
				case <-lc.ctx.Done():
					return
				case lc.queue <- link:
				default:
				}
			}
			log.Printf("Extracted %d links from %s", len(links), u)
		}
	}
}

// fetchBody retrieves the body of a URL with security validations.
func (lc *LocalCrawler) fetchBody(u string) ([]byte, error) {
	// Load config for security validation
	config, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Validate URL to prevent SSRF
	parsedURL, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	
	// Only allow HTTP/HTTPS schemes
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme: %s", parsedURL.Scheme)
	}
	
	// Validate against allowed domains
	allowed := false
	for _, domain := range config.AllowedDomains {
		if strings.Contains(parsedURL.Host, domain) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("domain not allowed: %s", parsedURL.Host)
	}

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	
	req.Header.Set("User-Agent", config.CrawlerUserAgent)

	res, err := lc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", res.StatusCode)
	}

	log.Printf("Crawled: %s", u)
	return io.ReadAll(res.Body)
}

// extractLinks parses HTML and extracts all Wikipedia links.
func (lc *LocalCrawler) extractLinks(body []byte, baseURLStr string) []string {
	base, _ := url.Parse(baseURLStr)
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}

	var links []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					if full := lc.resolveURL(a.Val, base); full != "" && strings.Contains(full, "wikipedia.org") {
						links = append(links, full)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return links
}

// resolveURL converts relative URLs to absolute ones and filters by scheme/host.
func (lc *LocalCrawler) resolveURL(href string, base *url.URL) string {
	u, err := url.Parse(href)
	if err != nil || (u.Host != "" && u.Host != base.Host) {
		return ""
	}
	resolved := base.ResolveReference(u)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	resolved.Fragment = ""
	resolved.RawQuery = ""
	return resolved.String()
}

// Close cleans up local crawler resources.
func (lc *LocalCrawler) Close() error {
	lc.cancel()
	close(lc.queue)
	return nil
}

// initialUrlSeed returns the seed URLs for crawling.
func initialUrlSeed() []string {
	config, err := LoadConfig()
	if err != nil {
		log.Printf("Failed to load config, using defaults: %v", err)
		return []string{
			"https://en.wikipedia.org/wiki/Ramayana",
			"https://en.wikipedia.org/wiki/Jai_Shri_Ram",
		}
	}

	// Generate seed URLs from allowed domains
	var seedURLs []string
	for _, domain := range config.AllowedDomains {
		if strings.Contains(domain, "wikipedia.org") {
			seedURLs = append(seedURLs, 
				"https://en.wikipedia.org/wiki/Ramayana",
				"https://en.wikipedia.org/wiki/Jai_Shri_Ram",
				"https://en.wikipedia.org/wiki/Mahabharata",
				"https://en.wikipedia.org/wiki/Hanuman",
			)
		} else if strings.Contains(domain, "indiatoday.in") {
			seedURLs = append(seedURLs, "https://www.indiatoday.in/")
		}
	}

	if len(seedURLs) == 0 {
		// Fallback to default
		seedURLs = []string{"https://en.wikipedia.org/wiki/Ramayana"}
	}

	return seedURLs
}

// getCrawlerEngine instantiates the appropriate crawler based on configuration.
func getCrawlerEngine(engine string, rdb *redis.Client, bloomKey string) (Crawler, error) {
	config, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	switch strings.ToLower(engine) {
	case "cloudflare":
		cfConfig := cfcrawler.CrawlerConfig{
			APIToken:       config.CloudflareAPIToken,
			AccountID:      config.CloudflareAccountID,
			PollInterval:   config.CloudflarePollInterval,
			MaxWaitTime:    config.CloudflareMaxWaitTime,
			ReturnFormat:   "markdown",
			AllowedDomains: config.AllowedDomains,
			MaxPages:       config.MaxPages,
		}

		crawler := cfcrawler.NewCrawler(cfConfig, rdb, bloomKey)
		return Crawler(crawler), nil

	case "local", "":
		return NewLocalCrawler(config.CrawlerWorkers, config.CrawlerPoliteness, rdb, bloomKey), nil

	default:
		return nil, fmt.Errorf("unknown crawler engine: %s", engine)
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize Redis
	rdb, err := db.RedisInit(ctx)
	if err != nil {
		log.Fatal("Redis connection failed:", err)
	}
	defer rdb.Close()

	// Initialize Bloom Filter
	bfKey := "wiki_bf_2025"
	if err := db.InitializeBloomFilter(ctx, rdb, bfKey, 0.001, 100000); err != nil {
		log.Fatal("Bloom filter init failed:", err)
	}

	// Instantiate the appropriate crawler
	crawler, err := getCrawlerEngine(config.CrawlerEngine, rdb, bfKey)
	if err != nil {
		log.Fatalf("Failed to initialize crawler: %v", err)
	}
	defer crawler.Close()

	// Graceful shutdown handler
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		log.Println("\nShutting down gracefully...")
		cancel()
		crawler.Close()
		os.Exit(0)
	}()

	// Get seed URLs
	seedURLs := initialUrlSeed()
	log.Printf("Starting %s crawler with %d seed URLs", config.CrawlerEngine, len(seedURLs))

	// Start crawling
	if err := crawler.Crawl(ctx, seedURLs); err != nil {
		log.Fatalf("Crawl failed: %v", err)
	}
}
