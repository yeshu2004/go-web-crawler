package main

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
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github/yeshu2004/go-epics/compress"
	db "github/yeshu2004/go-epics/db"
	tp "github/yeshu2004/go-epics/types"
	"github/yeshu2004/go-epics/nats"


	"github.com/dgraph-io/badger/v4"
	"github.com/redis/go-redis/v9"
	"golang.org/x/net/html"
)


func newTupleEvent(w string, c int, url string) *tp.TupleEvent{
	return &tp.TupleEvent{
		Word: w,
		Count: c,
		URLHash: url,
	}
}

func initialUrlSeed() []string {
	return []string{
		// "https://en.wikipedia.org/wiki/Hindus",
		// "https://www.indiatoday.in/",
		// "http://finetranscendentsublimeeclipse.neverssl.com/online/", // best for word testing
		"http://quotes.toscrape.com",
	}
}

type Client struct {
	badgerDb *badger.DB
	redisDB  *redis.Client
	nats 	*nats.Client
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
	indexerWorkers   = 4
	politeness = 800 * time.Millisecond
	bfKey      = "wiki_bf_2025"
)

func (c *Client) worker(ctx context.Context, rdb *redis.Client) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case url, ok := <-queue:
			if !ok {
				log.Println("worker exiting: queue closed")
				return
			}

			time.Sleep(politeness)

			body, err := fetchBody(url) // helper function used
			if err != nil {
				log.Printf("failed %s: %v", url, err)
				continue
			}

			key := hashURL(url)

			// we have raw body and we have to make in memory hash for itteration
			// also we can only put the hash key if it's length is more then 2(bcz most
			// valuable words are greater than 2 in length or say has length 3 or more)
			// in memory hash -> key: word, value: count

			// then write in memTbale -> key: word, value: [].append("hashurl"-> count);

			text := extractText(body) // extracts the text from the html page
			if len(text) == 0 {

			}

			// IDEA 1:

			// what if i docouple here and push every text to a message queue and then consume it
			// i.e push evry word/text into the NATS stream and then we would have have workers like
			// 4 diffent workers, each woker having word range like w1-> char 'a' to 'g' and so on
			// Then each worker would build Freq Map and then would buffer in memory and then push into
			// DB, updating the DB freq, like information retrival

			freqMap := buildFreqMap(text) // builds a word coud freq map
			if err := c.publishTuple(ctx, freqMap, key); err != nil{  // IDEA 3
				log.Fatalln(err);
			}

			// IDEA 2:

			// push freq map in a message queue and then each woker would have to consume the freq map
			// from the queue based on sharding logic and then they would buffer and combine multiple
			// message queue in memory and when each server reaches 60% memory or after 10sec each worker
			// has to update the freq of each word present in buffer into the DB

			// uncomment this, for logging purpose.
			fmt.Println(freqMap)
			// time.Sleep(5*time.Second)

			// type Posting struct {
			// 	URLHash string
			// 	Freq    int
			// }

			// var postings map[string][]Posting

			compressedBody, err := compress.GzipCompress(body)
			if err != nil {
				log.Fatalf("compression error: %v", err)
			}

			if err := c.badgerDb.Update(func(txn *badger.Txn) error {
				return txn.Set([]byte(key), compressedBody)
			}); err != nil {
				log.Printf("failed to store in BadgerDB: %v", err)
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

func (c *Client) publishTuple(ctx context.Context,freqMap map[string]int, URLHash string) error {
	for word, count := range freqMap {
		partitionID := partitionFor(word, indexerWorkers)

		tuple := newTupleEvent(word, count, URLHash);
		b, err := json.Marshal(tuple);
		if err != nil{
			return fmt.Errorf("error in tuple byte conversion: %v", err);
		}

		if err := c.nats.PublishTupleEvent(ctx, partitionID, b); err != nil{
			return fmt.Errorf("url(%s) tuple publish error: %v", URLHash, err)
		}

		log.Printf("Word (%s), Freq (%d) publish to partitionID:%d \n", tuple.Word, tuple.Count, partitionID);
	}
	return nil;
}

// partitionFor returns the indexer partition index for a given word.
// hash(word) % n — deterministic, so the same word always lands on the same worker.
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
	baddgerDB, err := badger.Open(badger.LSMOnlyOptions("./crwal_db"))
	if err != nil {
		log.Fatal("BadgerDB connection failed:", err)
	}
	defer baddgerDB.Close()

	// NATS connection
	nats , err := nats.NewNATSConn();
	if err != nil {
		log.Fatal("Nats connection failed:", err)
	}

	if err := nats.CreateTupleStream(ctx); err != nil{
		log.Println(err);
	}
	log.Println("Nats Tuple Stream connection sucessfull...")

	cli := &Client{
		badgerDb: baddgerDB,
		redisDB:  rdb,
		nats: nats,
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
