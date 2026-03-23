package models

import "time"

// PostingEvent is the payload produced by crawlers and consumed by indexers.
type PostingEvent struct {
	SourceID  string         `json:"source_id"`
	URL       string         `json:"url"`
	URLHash   string         `json:"url_hash"`
	Terms     map[string]int `json:"terms"`
	CrawledAt time.Time      `json:"crawled_at"`
}
