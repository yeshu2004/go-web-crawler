package crawler

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
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"
	"golang.org/x/net/html"

	"github/yeshu2004/go-epics/pkg/models"
	"github/yeshu2004/go-epics/pkg/producer"
)

type Config struct {
	ID         string
	Workers    int
	MaxPages   int64
	Politeness time.Duration
	Seeds      []string
	BloomKey   string
}

type Engine struct {
	cfg      Config
	http     *http.Client
	producer producer.Producer
	redis    *redis.Client
	queue    chan string
	seenMu   sync.Mutex
	seenMap  map[string]struct{}
	seenCnt  atomic.Int64
}

func New(cfg Config, p producer.Producer, redisClient *redis.Client) *Engine {
	if cfg.Workers <= 0 {
		cfg.Workers = 8
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 1000
	}
	if cfg.Politeness <= 0 {
		cfg.Politeness = 500 * time.Millisecond
	}
	if cfg.BloomKey == "" {
		cfg.BloomKey = "crawler_bf"
	}
	if len(cfg.Seeds) == 0 {
		cfg.Seeds = []string{"https://en.wikipedia.org/wiki/Hindus"}
	}
	if cfg.ID == "" {
		cfg.ID = "crawler-1"
	}

	return &Engine{
		cfg:      cfg,
		http:     &http.Client{Timeout: 20 * time.Second},
		producer: p,
		redis:    redisClient,
		queue:    make(chan string, 4096),
		seenMap:  make(map[string]struct{}),
	}
}

func (e *Engine) Start(ctx context.Context) error {
	for _, seed := range e.cfg.Seeds {
		ok, err := e.markIfNew(ctx, seed)
		if err != nil {
			return err
		}
		if ok {
			e.queue <- seed
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < e.cfg.Workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			e.worker(ctx, workerID)
		}(i + 1)
	}

	<-ctx.Done()
	wg.Wait()
	return nil
}

func (e *Engine) worker(ctx context.Context, workerID int) {
	for {
		if e.seenCnt.Load() >= e.cfg.MaxPages {
			return
		}

		select {
		case <-ctx.Done():
			return
		case u := <-e.queue:
			if u == "" {
				continue
			}
			time.Sleep(e.cfg.Politeness)
			e.crawlOne(ctx, workerID, u)
		}
	}
}

func (e *Engine) crawlOne(ctx context.Context, workerID int, u string) {
	body, err := e.fetchBody(u)
	if err != nil {
		log.Printf("worker=%d fetch failed: %s err=%v", workerID, u, err)
		return
	}

	text := extractText(body)
	terms := buildFreqMap(text)
	if len(terms) > 0 {
		event := &models.PostingEvent{
			SourceID:  e.cfg.ID,
			URL:       u,
			URLHash:   hashURL(u),
			Terms:     terms,
			CrawledAt: time.Now().UTC(),
		}
		if err := e.producer.Publish(ctx, event); err != nil {
			log.Printf("worker=%d publish failed: url=%s err=%v", workerID, u, err)
		}
	}

	for _, link := range extractLinks(body, u) {
		if e.seenCnt.Load() >= e.cfg.MaxPages {
			return
		}
		ok, err := e.markIfNew(ctx, link)
		if err != nil {
			log.Printf("mark url failed: %s err=%v", link, err)
			continue
		}
		if !ok {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case e.queue <- link:
		default:
		}
	}
}

func (e *Engine) fetchBody(u string) ([]byte, error) {
	// SSRF Protection: Validate URL before making request
	if err := validateURL(u); err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	// Use configurable User-Agent instead of hardcoded value
	userAgent := os.Getenv("CRAWLER_USER_AGENT")
	if userAgent == "" {
		userAgent = "go-epics-crawler/1.0"
	}
	req.Header.Set("User-Agent", userAgent)

	res, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

func (e *Engine) markIfNew(ctx context.Context, u string) (bool, error) {
	hash := hashURL(u)
	if e.redis != nil {
		added, err := e.redis.BFAdd(ctx, e.cfg.BloomKey, hash).Result()
		if err != nil {
			return false, err
		}
		if added {
			e.seenCnt.Add(1)
		}
		return added, nil
	}

	e.seenMu.Lock()
	defer e.seenMu.Unlock()
	if _, ok := e.seenMap[hash]; ok {
		return false, nil
	}
	e.seenMap[hash] = struct{}{}
	e.seenCnt.Add(1)
	return true, nil
}

func hashURL(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
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
		hasLetter := false
		for _, r := range word {
			if unicode.IsLetter(r) {
				hasLetter = true
				break
			}
		}
		if hasLetter {
			freqMap[word]++
		}
	}
	return freqMap
}

func extractText(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
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

func extractLinks(body []byte, baseURL string) []string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	links := make([]string, 0, 64)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				if full := resolveURL(a.Val, base); full != "" {
					links = append(links, full)
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

func resolveURL(href string, base *url.URL) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(u)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

// validateURL prevents SSRF attacks by validating URLs against allowlist
func validateURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("malformed URL: %w", err)
	}

	// Only allow HTTP/HTTPS schemes
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}

	// Block private/internal IP ranges to prevent SSRF
	if isPrivateOrLocalhost(parsed.Host) {
		return fmt.Errorf("private/internal host not allowed: %s", parsed.Host)
	}

	// Allowlist of permitted domains (configurable via env)
	allowedDomains := getAllowedDomains()
	for _, domain := range allowedDomains {
		if strings.HasSuffix(parsed.Host, domain) {
			return nil
		}
	}

	return fmt.Errorf("domain not in allowlist: %s", parsed.Host)
}

// getAllowedDomains returns list of allowed domains from env or defaults
func getAllowedDomains() []string {
	envDomains := os.Getenv("CRAWLER_ALLOWED_DOMAINS")
	if envDomains != "" {
		return strings.Split(envDomains, ",")
	}
	// Default allowlist
	return []string{
		"en.wikipedia.org",
		"www.indiatoday.in",
		"wikipedia.org",
		"indiatoday.in",
	}
}

// isPrivateOrLocalhost checks if host is private/internal IP or localhost
func isPrivateOrLocalhost(host string) bool {
	// Remove port if present
	if colonIndex := strings.LastIndex(host, ":"); colonIndex != -1 {
		host = host[:colonIndex]
	}

	// Check for localhost variants
	localHosts := []string{"localhost", "127.0.0.1", "::1", "0.0.0.0"}
	for _, local := range localHosts {
		if host == local {
			return true
		}
	}

	// Check for private IP ranges
	privateRanges := []string{
		"10.",      // 10.0.0.0/8
		"172.16.",  // 172.16.0.0/12 (simplified)
		"192.168.", // 192.168.0.0/16
		"169.254.", // Link-local
	}

	for _, prefix := range privateRanges {
		if strings.HasPrefix(host, prefix) {
			return true
		}
	}

	return false
}
