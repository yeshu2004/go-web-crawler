package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Redis Configuration
	RedisURL string

	// Cloudflare Configuration
	CloudflareAPIToken  string
	CloudflareAccountID string
	CloudflarePollInterval time.Duration
	CloudflareMaxWaitTime  time.Duration

	// Crawler Configuration
	CrawlerEngine     string
	CrawlerWorkers    int
	CrawlerPoliteness time.Duration
	CrawlerUserAgent  string

	// Security Configuration
	AllowedDomains []string
	MaxPages       int
}

func LoadConfig() (*Config, error) {
	config := &Config{
		// Default values
		RedisURL:               getEnvOrDefault("REDIS_URL", "redis://localhost:6379"),
		CrawlerEngine:          getEnvOrDefault("CRAWLER_ENGINE", "local"),
		CrawlerWorkers:         getEnvIntOrDefault("CRAWLER_WORKERS", 8),
		CrawlerPoliteness:      time.Duration(getEnvIntOrDefault("CRAWLER_POLITENESS_MS", 800)) * time.Millisecond,
		CrawlerUserAgent:       getEnvOrDefault("CRAWLER_USER_AGENT", "GoWebCrawler/1.0"),
		CloudflarePollInterval: time.Duration(getEnvIntOrDefault("CLOUDFLARE_POLL_INTERVAL", 15)) * time.Second,
		CloudflareMaxWaitTime:  time.Duration(getEnvIntOrDefault("CLOUDFLARE_MAX_WAIT_TIME", 604800)) * time.Second, // 7 days
		MaxPages:               getEnvIntOrDefault("MAX_PAGES", 1000),
	}

	// Required for Cloudflare engine
	if config.CrawlerEngine == "cloudflare" {
		config.CloudflareAPIToken = os.Getenv("CLOUDFLARE_API_TOKEN")
		config.CloudflareAccountID = os.Getenv("CLOUDFLARE_ACCOUNT_ID")

		if config.CloudflareAPIToken == "" || config.CloudflareAccountID == "" {
			return nil, fmt.Errorf("CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID are required for cloudflare engine")
		}
	}

	// Parse allowed domains
	domainsStr := getEnvOrDefault("ALLOWED_DOMAINS", "wikipedia.org,indiatoday.in")
	config.AllowedDomains = strings.Split(domainsStr, ",")
	for i, domain := range config.AllowedDomains {
		config.AllowedDomains[i] = strings.TrimSpace(domain)
	}

	return config, nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}