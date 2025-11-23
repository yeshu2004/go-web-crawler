package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	_ "github.com/lib/pq"
	"golang.org/x/net/html"
)

var pgDB *sql.DB

type PageAnalysis struct {
	URL              string   `json:"url"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	HeadingCount     int      `json:"heading_count"`
	ParagraphCount   int      `json:"paragraph_count"`
	ImageCount       int      `json:"image_count"`
	InternalLinks    int      `json:"internal_links"`
	ExternalLinks    int      `json:"external_links"`
	WordCount        int      `json:"word_count"`
	TextContent      string   `json:"text_content"`
	HeadingTitles    []string `json:"heading_titles"`
	CrawledAt        string   `json:"crawled_at"`
}

type PagesResponse struct {
	Total int              `json:"total"`
	Pages []PageSummary    `json:"pages"`
}

type PageSummary struct {
	ID         int    `json:"id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	LinksCount int    `json:"links_count"`
	CrawledAt  string `json:"crawled_at"`
}

func initDB() (*sql.DB, error) {
	connStr := "user=postgres password=yourpassword dbname=crawler_db sslmode=disable"
	database, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	if err := database.Ping(); err != nil {
		return nil, err
	}

	log.Println("PostgreSQL connected")
	return database, nil
}

func analyzeHTML(htmlContent string) *PageAnalysis {
	doc, err := html.Parse(bytes.NewReader([]byte(htmlContent)))
	if err != nil {
		return nil
	}

	analysis := &PageAnalysis{
		HeadingTitles: []string{},
	}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					analysis.Title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				for _, a := range n.Attr {
					if a.Key == "name" && a.Val == "description" {
						for _, b := range n.Attr {
							if b.Key == "content" {
								analysis.Description = b.Val
							}
						}
					}
				}
			case "h1", "h2", "h3", "h4", "h5", "h6":
				analysis.HeadingCount++
				if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					analysis.HeadingTitles = append(analysis.HeadingTitles, strings.TrimSpace(n.FirstChild.Data))
				}
			case "p":
				analysis.ParagraphCount++
				if n.FirstChild != nil {
					analysis.WordCount += len(strings.Fields(extractText(n)))
					analysis.TextContent += extractText(n) + " "
				}
			case "img":
				analysis.ImageCount++
			case "a":
				analysis.InternalLinks++
				for _, a := range n.Attr {
					if a.Key == "href" && (strings.HasPrefix(a.Val, "http://") || strings.HasPrefix(a.Val, "https://")) {
						analysis.ExternalLinks++
						analysis.InternalLinks--
						break
					}
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)
	analysis.TextContent = strings.TrimSpace(analysis.TextContent)
	if len(analysis.TextContent) > 500 {
		analysis.TextContent = analysis.TextContent[:500] + "..."
	}

	return analysis
}

func extractText(n *html.Node) string {
	var text string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text += n.Data + " "
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(text)
}

// Handler: Analyze a specific page by URL
func handleAnalyzePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	pageURL := r.URL.Query().Get("url")
	if pageURL == "" {
		http.Error(w, `{"error":"url parameter required"}`, http.StatusBadRequest)
		return
	}

	var htmlContent string
	var title string
	var crawledAt string

	query := `SELECT html_content, title, crawled_at FROM crawled_pages WHERE url = $1`
	err := pgDB.QueryRow(query, pageURL).Scan(&htmlContent, &title, &crawledAt)
	if err == sql.ErrNoRows {
		http.Error(w, `{"error":"page not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}

	analysis := analyzeHTML(htmlContent)
	analysis.URL = pageURL
	analysis.Title = title
	analysis.CrawledAt = crawledAt

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(analysis)
}

// Handler: Get all crawled pages
func handleGetAllPages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	rows, err := pgDB.Query(`SELECT id, url, title, links_count, crawled_at FROM crawled_pages ORDER BY crawled_at DESC`)
	if err != nil {
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	pages := []PageSummary{}
	for rows.Next() {
		var page PageSummary
		if err := rows.Scan(&page.ID, &page.URL, &page.Title, &page.LinksCount, &page.CrawledAt); err != nil {
			log.Printf("Row scan error: %v", err)
			continue
		}
		pages = append(pages, page)
	}

	response := PagesResponse{
		Total: len(pages),
		Pages: pages,
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// Handler: Get statistics
func handleGetStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var totalPages int
	var totalLinks int

	pgDB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(links_count), 0) FROM crawled_pages`).Scan(&totalPages, &totalLinks)

	stats := map[string]interface{}{
		"total_pages":    totalPages,
		"total_links":    totalLinks,
		"avg_links":      float64(totalLinks) / float64(totalPages),
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(stats)
}

// Handler: Search pages
func handleSearchPages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, `{"error":"q parameter required"}`, http.StatusBadRequest)
		return
	}

	searchTerm := "%" + query + "%"
	rows, err := pgDB.Query(`
		SELECT id, url, title, links_count, crawled_at 
		FROM crawled_pages 
		WHERE url ILIKE $1 OR title ILIKE $1 
		ORDER BY crawled_at DESC
	`, searchTerm)
	
	if err != nil {
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	pages := []PageSummary{}
	for rows.Next() {
		var page PageSummary
		if err := rows.Scan(&page.ID, &page.URL, &page.Title, &page.LinksCount, &page.CrawledAt); err != nil {
			continue
		}
		pages = append(pages, page)
	}

	response := PagesResponse{
		Total: len(pages),
		Pages: pages,
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func main() {
	var err error
	pgDB, err = initDB()
	if err != nil {
		log.Fatal("Database connection failed:", err)
	}
	defer pgDB.Close()

	// Routes
	http.HandleFunc("/api/analyze", handleAnalyzePage)
	http.HandleFunc("/api/pages", handleGetAllPages)
	http.HandleFunc("/api/search", handleSearchPages)
	http.HandleFunc("/api/stats", handleGetStats)

	log.Println("API Server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}