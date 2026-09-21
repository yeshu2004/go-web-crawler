package crawler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github/yeshu2004/go-epics/compress"
	"github/yeshu2004/go-epics/db"

	"github/yeshu2004/go-epics/nats"
	tp "github/yeshu2004/go-epics/types"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/net/html"
)

var (
	client   = &http.Client{Timeout: 30 * time.Second}
	expected = 10000000
	fp_rate  = 0.001
)

var enqueueIfNewScript = redis.NewScript(`
	if redis.call("BF.EXISTS", KEYS[1], ARGV[1]) == 0 then
        redis.call("BF.ADD", KEYS[1], ARGV[1])
		redis.call("LPUSH", KEYS[2], ARGV[2])
        return 1
    end

    return 0
`)

const (
	workers        = 8
	indexerWorkers = 4
	politeness     = 800 * time.Millisecond
)

type Crawler struct {
	Id       string
	queueKey string
	rdb      *redis.Client
	badger   *badger.DB
	nats     *nats.Client
	bfKey    string

	// queue          chan string
	wg             sync.WaitGroup
	duplicateCount atomic.Int64
}

func newTupleEvent(id string, w string, c int, url string) *tp.TupleEvent {
	return &tp.TupleEvent{
		Id:      id,
		Word:    w,
		Count:   c,
		URLHash: url,
	}
}

// PROBLEM: Deadlock. Workers do a blocking c.queue <- link. Once 10,000 URLs are queued,
// all 8 workers can be blocked sending and none is left to receive.
// PROPOSED SOLN: Redis becomes the frontier/source of truth, and the Go workers
// consume directly from it.
func (c *Crawler) worker(ctx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return

		default:
			url, err := c.dequeue(ctx)
			if err != nil {
				if err == ctx.Err() {
					return
				}
				log.Println("redis error:", err)
				continue
			}

			body, err := fetchBody(url) // helper function used
			if err != nil {
				log.Printf("failed %s: %v", url, err)
				continue
			}

			key := hashURL(url)

			text := extractText(body) // extracts the text from the html page
			if len(text) == 0 {

			}

			freqMap := buildFreqMap(text)                             // builds a word coud freq map
			if err := c.publishTuple(ctx, freqMap, key); err != nil { // IDEA 3
				log.Println(err)
			}

			// uncomment this, for logging purpose.
			// fmt.Println(freqMap)
			// time.Sleep(5*time.Second)

			compressedBody, err := compress.GzipCompress(body)
			if err != nil {
				log.Printf("compression error: %v", err)
			}

			if err := c.badger.Update(func(txn *badger.Txn) error {
				return txn.Set([]byte(key), compressedBody)
			}); err != nil {
				log.Printf("failed to store in BadgerDB: %v", err)
			}

			links := extractLinks(body, url) // helper function used

			for _, link := range links {

				enqueued, err := c.enqueuIfNew(ctx, link)
				if err != nil {
					log.Printf("failed to enqueue link-%s: %v", link, err)
					continue
				}

				if !enqueued {
					total := c.duplicateCount.Add(1)

					if total <= 100 || total%5000 == 0 {
						log.Printf("Duplicate skipped (%d total): %s", total, link)
					}

					continue
				}
			}
			log.Printf("Extracted %d links from %s", len(links), url)
		}
	}
}


func (c *Crawler) publishTuple(ctx context.Context, freqMap map[string]int, URLHash string) error {
	buckets := make(map[int][]tp.TupleEvent, indexerWorkers);

	for word, count := range freqMap{
		pid := partitionFor(word, indexerWorkers)
		tuple := newTupleEvent(uuid.NewString(), word, count, URLHash)
		buckets[pid] = append(buckets[pid], *tuple)
	}

	for pid, events := range buckets{
		b, err := json.Marshal(events)
        if err != nil {
            return fmt.Errorf("marshal batch pid=%d: %w", pid, err)
        }
        if err := c.nats.PublishTupleEvent(ctx, pid, b); err != nil {
            return fmt.Errorf("publish batch pid=%d: %w", pid, err)
        }
	}
	// for word, count := range freqMap {
	// 	partitionID := partitionFor(word, indexerWorkers)

	// 	id := uuid.New().String()
	// 	tuple := newTupleEvent(id, word, count, URLHash)
	// 	b, err := json.Marshal(tuple)
	// 	if err != nil {
	// 		return fmt.Errorf("error in tuple conversion: %v", err)
	// 	}

	// 	if err := c.nats.PublishTupleEvent(ctx, partitionID, b); err != nil {
	// 		return fmt.Errorf("url(%s) tuple publish error: %v", URLHash, err)
	// 	}

	// 	log.Printf("Word (%s), Freq (%d) publish to partitionID:%d \n", tuple.Word, tuple.Count, partitionID)
	// }
	return nil
}

// partitionFor returns the indexer partition index for a given word.
// hash(word) % n — deterministic, so the same word always lands on the same worker.
// Q) what happend if the worker gets down i.e. change in value of n
// Ans) consistent hashsing T.B.D
func partitionFor(word string, n int) int {
	h := sha256.Sum256([]byte(word))
	// Use first 8 bytes as uint64 to avoid bias
	v := uint64(h[0])<<56 | uint64(h[1])<<48 | uint64(h[2])<<40 | uint64(h[3])<<32 |
		uint64(h[4])<<24 | uint64(h[5])<<16 | uint64(h[6])<<8 | uint64(h[7])
	return int(v % uint64(n))
}

func buildFreqMap(text string) map[string]int {
	freqMap := make(map[string]int)
	text = strings.ToLower(text)

	reg := regexp.MustCompile(`[^\p{L}\p{N}]+`)
	words := reg.Split(text, -1)

	for _, word := range words {
		if len(word) < 3 {
			continue
		}

		hasVaildLetter := false
		for _, r := range word {
			if unicode.IsLetter(r) {
				hasVaildLetter = true
			}
		}

		if hasVaildLetter {
			freqMap[word]++
		}
	}

	return freqMap
}

func extractText(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		log.Println(err)
		return ""
	}

	var textBuilder strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			textBuilder.WriteString(n.Data)
			textBuilder.WriteString(" ")
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return textBuilder.String()
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

func NewCrawler(ctx context.Context, rdb *redis.Client, badger *badger.DB, nats *nats.Client) (*Crawler, error) {
	id := uuid.NewString()
	bfKey := "crawler:" + id + ":bloom"
	queueID := fmt.Sprintf("queue:%s", id)

	if err := db.InitializeBloomFilterTest(ctx, rdb, bfKey, fp_rate, int64(expected)); err != nil {
		log.Println("Bloom filter init failed:", err)
		return nil, err
	}

	return &Crawler{
		Id:       id,      // each crawler will have unique id
		queueKey: queueID, // ... unique independent queue
		rdb:      rdb,     // shared
		badger:   badger,  // shared
		nats:     nats,    // shared
		bfKey:    bfKey,   // unqiue
		// queue:    make(chan string, 10000), // unique (not required now)
	}, nil
}

func (c *Crawler) Run(ctx context.Context, urlSeeds []string) {
	for _, seed := range urlSeeds {

		enqueued, err := c.enqueuIfNew(ctx, seed)
		if err != nil {
			log.Printf("Failed to enqueue %s: %v", seed, err)
			continue
		}

		if !enqueued {
			c.duplicateCount.Add(1)
		}
	}

	// start workers
	for i := 0; i < workers; i++ {
		c.wg.Add(1)
		go c.worker(ctx) // <- entry point for worker
	}

	c.wg.Wait()
	log.Println("Crawl completed successfully!")
}

// enqueuIfNew first checks through the bloom filter, if the link is not visited then
// it is enqueued in the queue, other vise not. This is done in "procedural" way i.e.
// those two operations happen atomically from Redis's point of view.
func (c *Crawler) enqueuIfNew(ctx context.Context, link string) (bool, error) {
	urlHash := hashURL(link)
	n, err := enqueueIfNewScript.Run(ctx, c.rdb, []string{c.bfKey, c.queueKey}, urlHash, link).Int();
	if err != nil {
		return false, err
	}

	return n == 1, nil
}

func (c *Crawler) dequeue(ctx context.Context) (string, error) {
	res, err := c.rdb.BRPop(ctx, 5*time.Second, c.queueKey).Result()
	if err != nil {
		return "", err
	}
	url := res[1]
	return url, nil
}

// 	OLD/NOT REQUIRED CODE:

// NOT IN USE NOW AS UPDATED CODE DOES THAT IN ONE ATOMIC OPERATION
func (c *Crawler) claimURL(ctx context.Context, url string) (bool, error) {
	hash := hashURL(url)

	result, err := claimURLScript.Run(ctx, c.rdb, []string{c.bfKey}, hash).Int()

	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		return false, fmt.Errorf("failed to claim URL: %w", err)
	}

	return result == 1, nil
}

// REPLACED WITH ENQUEUEIFEXISTS
func (c *Crawler) enqueue(ctx context.Context, link string) {
	if err := c.rdb.LPush(ctx, c.queueKey, link).Err(); err != nil {
		log.Printf("redis enqueue failed: %v\n", err)
		return
	}
}

// WROTE NEW LUA SCRIPT FOR ATOMIC OPERATION
var claimURLScript = redis.NewScript(`
    if redis.call("BF.EXISTS", KEYS[1], ARGV[1]) == 0 then
        redis.call("BF.ADD", KEYS[1], ARGV[1])
        return 1
    end

    return 0
`)

// NOT REQUIRED AS CLIENT WILL SEND THE URL_SEEDS
func initialUrlSeed() []string {
	return []string{
		"https://en.wikipedia.org/wiki/Hindus",
		// "https://www.indiatoday.in/",
		// "http://finetranscendentsublimeeclipse.neverssl.com/online/", // best for word testing
		// "http://quotes.toscrape.com",
	}
}

// REPLACED WITH REDIS LUA SCIRPT: Check's if Bloom Filter has the url(Redis).
func (c *Crawler) seenBefore(ctx context.Context, rdb *redis.Client, url string) bool {
	hash := hashURL(url)
	exists, err := rdb.BFExists(ctx, c.bfKey, hash).Result()
	if err != nil {
		if ctx.Err() == context.Canceled {
			return true
		}
		log.Printf("BFExists error: %v", err)
		return true
	}
	return exists
}

// REPLACED WITH REDIS LUA SCIRPT: marks the url in Bloom Filter(Redis).
func (c *Crawler) markSeen(ctx context.Context, rdb *redis.Client, url string) {
	hash := hashURL(url)
	if err := rdb.BFAdd(ctx, c.bfKey, hash).Err(); err != nil {
		log.Printf("BFAdd failed for %s: %v", url, err)
	}
}
