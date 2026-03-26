package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	db "github/yeshu2004/go-epics/db"
	"github/yeshu2004/go-epics/pkg/crawler"
	"github/yeshu2004/go-epics/pkg/producer"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var prod producer.Producer
	if envBool("USE_KAFKA", false) {
		prod = producer.NewKafkaProducer(nil, env("KAFKA_TOPIC", "postings"), env("CRAWLER_ID", "crawler-1"))
	} else {
		producerNATS, err := producer.NewNATSProducer(
			env("NATS_URL", "nats://localhost:4222"),
			env("NATS_SUBJECT", "postings.documents"),
		)
		if err != nil {
			log.Fatalf("nats producer init failed: %v", err)
		}
		prod = producerNATS
	}
	defer prod.Close()

	rdb, err := db.RedisInit(ctx)
	if err != nil {
		log.Printf("redis unavailable, using in-memory seen set: %v", err)
		rdb = nil
	}
	if rdb != nil {
		defer rdb.Close()
		if err := db.InitializeBloomFilter(
			ctx,
			rdb,
			env("BLOOM_KEY", "crawler_bf"),
			0.001,
			int64(envInt("BLOOM_CAPACITY", 5000000)),
		); err != nil {
			log.Fatalf("bloom setup failed: %v", err)
		}
	}

	engine := crawler.New(crawler.Config{
		ID:         env("CRAWLER_ID", "crawler-1"),
		Workers:    envInt("CRAWLER_WORKERS", 8),
		MaxPages:   int64(envInt("MAX_PAGES", 10000)),
		Politeness: time.Duration(envInt("POLITENESS_MS", 400)) * time.Millisecond,
		Seeds:      splitCSV(env("SEEDS", "https://en.wikipedia.org/wiki/Hindus")),
		BloomKey:   env("BLOOM_KEY", "crawler_bf"),
	}, prod, rdb)

	log.Println("crawler started")
	if err := engine.Start(ctx); err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes"
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	res := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := strings.TrimSpace(part); s != "" {
			res = append(res, s)
		}
	}
	if len(res) == 0 {
		return []string{"https://en.wikipedia.org/wiki/Hindus"}
	}
	return res
}
