package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"sync/atomic"
	"time"

	"github/yeshu2004/go-epics/compress"
	db "github/yeshu2004/go-epics/db"

	"github.com/dgraph-io/badger/v4"
	"github.com/redis/go-redis/v9"
	"golang.org/x/net/html"
)

func initialUrlSeed() []string {
	return []string{
		"https://timesofindia.indiatimes.com/",
	}
}

type Client struct{
	badgerDb *badger.DB
	redisDB *redis.Client
}

var (
	duplicateCount atomic.Int64
	queue          = make(chan string, 10000)
	wg             sync.WaitGroup
	client         = &http.Client{Timeout: 30 * time.Second}
	expected       = 10000000
	fp_rate        = 0.001
)

const (
	workers    = 8
	politeness = 800 * time.Millisecond
	bfKey      = "wiki_bf_2025"
)

func (c *Client)worker(ctx context.Context, rdb *redis.Client) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case url, ok := <-queue:
			if !ok {
				log.Println("Worker exiting: queue closed")
				return
			}

			time.Sleep(politeness)

			body, err := fetchBody(url) // helper function used
			if err != nil {
				log.Printf("Failed %s: %v", url, err)
				continue
			}

			// TODO: store in db
			key := hashURL(url);
			compressedBody, err := compress.GzipCompress(body);
			if err != nil{
				log.Fatalf("compression error: %v", err);
			}

			if err := c.badgerDb.Update(func(txn *badger.Txn) error {
				return txn.Set([]byte(key), compressedBody);
			}); err != nil {
				log.Printf("Failed to store in BadgerDB: %v", err)
			}

			links := extractLinks(body, url) // helper function used

			for _, link := range links {
				// bloom filter check, if present skip-> for matrix
				hashed := hashURL(link)
				added, err := rdb.BFAdd(ctx, bfKey, hashed).Result()
				if err != nil {
					log.Printf("BFAdd error: %v", err)
					continue
				}

				if !added {
					total := duplicateCount.Add(1)
					if total <= 100 || total%5000 == 0 {
						log.Printf("Duplicate skipped (%d total): %s", total, link)
					}
					continue
				}

				select {
				case <-ctx.Done():
					return
				case queue <- link: // pushes link in queue
				default:
				}
			}
			log.Printf("Extracted %d links from %s", len(links), url)

		}
	}

}

// fetchBody returen the []byte i.e. res.Body and err if required,
// initally the req is send to link(string).
func fetchBody(u string) ([]byte, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "MyCollageProjectCrawler (https://github.com/yourname/my-crawler; yourname@example.com)")

	res, err := client.Do(req)
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

// parses the HTML doc and returns the slice of links extracted.
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
						if full != "" {
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

// Check's if Bloom Filter has the url(Redis).
func seenBefore(ctx context.Context, rdb *redis.Client, url string) bool {
	hash := hashURL(url)
	exists, err := rdb.BFExists(ctx, bfKey, hash).Result()
	if err != nil {
		if ctx.Err() == context.Canceled {
			return true
		}
		log.Printf("BFExists error: %v", err)
		return true
	}
	return exists
}

// marks the url in Bloom Filter(Redis).
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
	resolved.Path = strings.ToLower(resolved.Path)
	if resolved.Path != "" && !strings.HasSuffix(resolved.Path, "/") {
		resolved.Path = strings.TrimSuffix(resolved.Path, "/")
	}
	return resolved.String()
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// redis connection
	rdb, err := db.RedisInit(ctx)
	if err != nil {
		log.Fatal("Redis connection failed:", err)
	}
	defer rdb.Close()

	if err := db.InitializeBloomFilter(ctx, rdb, bfKey, fp_rate, int64(expected)); err != nil {
		log.Fatal("Bloom filter init failed:", err)
	}	

	// badgerDB connection
	baddgerDB, err := badger.Open(badger.LSMOnlyOptions("./crwal_db"));
	if err != nil {
		log.Fatal("BadgerDB connection failed:", err)
	}
	defer baddgerDB.Close()

	cli := &Client{
		badgerDb: baddgerDB,
		redisDB: rdb,
	}

	// handle graceful shutdown
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

	// start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go cli.worker(ctx, rdb) // <- entry point for worker
	}

	wg.Wait()
	log.Println("Crawl completed successfully!")
}
