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
	"time"

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
	queue  = make(chan string, 10000)
	wg     sync.WaitGroup
	client = &http.Client{Timeout: 30 * time.Second}
)

const (
	workers    = 8
	politeness = 800 * time.Millisecond
	bfKey      = "wiki_bf_2025"
)

func worker(ctx context.Context, rdb *redis.Client) {
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

			links := extractLinks(body, url) // helper function used

			for _, link := range links {
				if seenBefore(ctx, rdb, link) {
					continue
				}
				markSeen(ctx, rdb, link)

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
						if full != "" && strings.Contains(full, "wikipedia.org") {
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

// Check's if Bloom Filter has the url.
func seenBefore(ctx context.Context, rdb *redis.Client, url string) bool {
	hash := hashURL(url)
	exists, err := rdb.BFExists(ctx, bfKey, hash).Result()
	if err != nil {
		log.Printf("BFExists error: %v", err)
		return true
	}
	return exists
}

// marks the url in Bloom Filter.
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

	// start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker(ctx, rdb)
	}

	wg.Wait()
	log.Println("Crawl completed successfully!")
}
