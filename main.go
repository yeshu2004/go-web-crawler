package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	_"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	db "github/yeshu2004/go-epics/db"
	"golang.org/x/net/html"
)

func initialUrlSeed() []string {
	return []string{
		"https://en.wikipedia.org/wiki/Ramayana",
		"https://en.wikipedia.org/wiki/Jai_Shri_Ram",
		"https://en.wikipedia.org/wiki/Mahabharata",
		"https://en.wikipedia.org/wiki/Hanuman",
	}
}

var (
	queue     = make(chan string, 10000)
	wg        sync.WaitGroup
	client    = &http.Client{Timeout: 30 * time.Second}
	pgDB      *sql.DB
	dbMutex   sync.Mutex
)

const (
	workers    = 8
	politeness = 800 * time.Millisecond
	bfKey      = "wiki_bf_2025"
)

func initPostgres() (*sql.DB, error) {
	connStr := "user=postgres password=yourpassword dbname=crawler_db sslmode=disable"
	database, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	if err := database.Ping(); err != nil {
		return nil, err
	}

	createTableSQL := `
	CREATE TABLE IF NOT EXISTS crawled_pages (
		id SERIAL PRIMARY KEY,
		url VARCHAR(2048) UNIQUE NOT NULL,
		html_content TEXT NOT NULL,
		html_hash VARCHAR(64) NOT NULL,
		status_code INT,
		crawled_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		title VARCHAR(255),
		links_count INT
	);
	
	CREATE INDEX IF NOT EXISTS idx_url ON crawled_pages(url);
	CREATE INDEX IF NOT EXISTS idx_hash ON crawled_pages(html_hash);
	`

	if _, err := database.Exec(createTableSQL); err != nil {
		return nil, err
	}

	log.Println("PostgreSQL connected and tables initialized")
	return database, nil
}

func storeHTMLToDB(ctx context.Context, urlStr string, htmlContent []byte, statusCode int, linksCount int) error {
	hash := hashURL(urlStr)
	title := extractTitle(htmlContent)

	dbMutex.Lock()
	defer dbMutex.Unlock()

	insertSQL := `
	INSERT INTO crawled_pages (url, html_content, html_hash, status_code, title, links_count)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (url) DO UPDATE SET 
		html_content = EXCLUDED.html_content,
		status_code = EXCLUDED.status_code,
		crawled_at = CURRENT_TIMESTAMP
	`

	if _, err := pgDB.ExecContext(ctx, insertSQL, urlStr, string(htmlContent), hash, statusCode, title, linksCount); err != nil {
		log.Printf("Failed to store HTML for %s: %v", urlStr, err)
		return err
	}

	log.Printf("Stored HTML for: %s", urlStr)
	return nil
}

func extractTitle(htmlContent []byte) string {
	doc, err := html.Parse(bytes.NewReader(htmlContent))
	if err != nil {
		return ""
	}

	var title string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "title" {
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				title = n.FirstChild.Data
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return title
}

func worker(ctx context.Context, rdb *redis.Client) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case urlStr, ok := <-queue:
			if !ok {
				log.Println("Worker exiting: queue closed")
				return
			}

			time.Sleep(politeness)

			body, statusCode, err := fetchBody(urlStr)
			if err != nil {
				log.Printf("Failed %s: %v", urlStr, err)
				continue
			}

			// Store HTML in database
			links := extractLinks(body, urlStr)
			if err := storeHTMLToDB(ctx, urlStr, body, statusCode, len(links)); err != nil {
				log.Printf("Failed to store HTML: %v", err)
			}

			for _, link := range links {
				if seenBefore(ctx, rdb, link) {
					continue
				}
				markSeen(ctx, rdb, link)

				select {
				case <-ctx.Done():
					return
				case queue <- link:
				default:
				}
			}
			log.Printf("Extracted %d links from %s", len(links), urlStr)
		}
	}
}

func fetchBody(u string) ([]byte, int, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "MyCollageProjectCrawler (https://github.com/yourname/my-crawler; yourname@example.com)")

	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, fmt.Errorf("status %d", res.StatusCode)
	}

	log.Printf("Crawled: %s", u)
	body, err := io.ReadAll(res.Body)
	return body, res.StatusCode, err
}

func extractLinks(body []byte, baseURLStr string) []string {
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
					if full := resolveURL(a.Val, base); full != "" {
						if strings.Contains(full, "wikipedia.org") {
							links = append(links, full)
						}
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

func seenBefore(ctx context.Context, rdb *redis.Client, url string) bool {
	hash := hashURL(url)
	exists, err := rdb.BFExists(ctx, bfKey, hash).Result()
	if err != nil {
		log.Printf("BFExists error: %v", err)
		return true
	}
	return exists
}

func markSeen(ctx context.Context, rdb *redis.Client, url string) {
	hash := hashURL(url)
	if err := rdb.BFAdd(ctx, bfKey, hash).Err(); err != nil {
		log.Printf("BFAdd failed for %s: %v", url, err)
	}
}

func hashURL(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
}

func resolveURL(href string, base *url.URL) string {
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

func main() {
	var err error
	pgDB, err = initPostgres()
	if err != nil {
		log.Fatal("PostgreSQL connection failed:", err)
	}
	defer pgDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rdb, err := db.RedisInit(ctx)
	if err != nil {
		log.Fatal("Redis connection failed:", err)
	}
	defer rdb.Close()

	if err := db.InitializeBloomFilter(ctx, rdb, bfKey, 0.001, 100000); err != nil {
		log.Fatal("Bloom filter init failed:", err)
	}

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		log.Println("\nShutting down gracefully...")
		cancel()
		close(queue)
	}()

	for _, seed := range initialUrlSeed() {
		if !seenBefore(ctx, rdb, seed) {
			markSeen(ctx, rdb, seed)
			queue <- seed
		}
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker(ctx, rdb)
	}

	wg.Wait()
	log.Println("Crawl completed successfully!")
}