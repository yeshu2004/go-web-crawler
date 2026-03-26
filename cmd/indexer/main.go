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

	"github/yeshu2004/go-epics/pkg/consumer"
	"github/yeshu2004/go-epics/pkg/database"
	"github/yeshu2004/go-epics/pkg/indexer"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	repo, err := database.NewPostgresRepository(
		ctx,
		env("POSTGRES_URL", "postgres://postgres:postgres@localhost:5432/crawler?sslmode=disable"),
	)
	if err != nil {
		log.Fatalf("postgres init failed: %v", err)
	}
	defer repo.Close()

	batcher := indexer.NewBatcher(
		repo,
		envInt("MAX_BATCH_SIZE", 10000),
		time.Duration(envInt("FLUSH_INTERVAL_SECONDS", 5))*time.Second,
	)
	go batcher.RunPeriodicFlush(ctx)

	var cons consumer.Consumer
	if envBool("USE_KAFKA", false) {
		cons = consumer.NewKafkaConsumer(nil, env("KAFKA_TOPIC", "postings"), env("CONSUMER_GROUP", "indexers"))
	} else {
		natsCons, err := consumer.NewNATSConsumer(
			env("NATS_URL", "nats://localhost:4222"),
			env("NATS_SUBJECT", "postings.documents"),
			env("CONSUMER_GROUP", "indexers"),
		)
		if err != nil {
			log.Fatalf("nats consumer init failed: %v", err)
		}
		cons = natsCons
	}
	defer cons.Close()

	log.Println("indexer started")
	if err := cons.Start(ctx, batcher.Add); err != nil {
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
