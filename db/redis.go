package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
)

func InitializeBloomFilter(ctx context.Context, client *redis.Client, bloomKey string, errorRate float64, capacity int64) error {
	res, err := client.Exists(ctx, bloomKey).Result()
	if err != nil {
		log.Fatalf("failed to check if key exists: %v", err)
	}
	if res == 0 {
		if err := client.BFReserve(ctx, bloomKey, errorRate, capacity).Err(); err != nil {
			log.Fatalf("failed to create Bloom filter: %v", err)
		}
		fmt.Printf("succesfully initalized bloom filter: %v\n", bloomKey)
	}
	if res == 1 {
		fmt.Printf("bloom filter already initalized with the key: %v", bloomKey)
	}
	return nil
}

func RedisInit(ctx context.Context) (*redis.Client, error) {
	Client := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // No password set
		DB:       0,  // Use default DB
		Protocol: 2,  // Connection protocol
	})

	if err := Client.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v. Ensure Redis is running on localhost:6379.", err)
		return nil, err
	}
	log.Println("Redis connection successful")
	return Client, nil
}

// SeenBefore checks if a URL has been seen before using the Bloom Filter.
// Exported for use by multiple crawler implementations.
func SeenBefore(ctx context.Context, client *redis.Client, bloomKey, url string) bool {
	hash := hashURL(url)
	exists, err := client.BFExists(ctx, bloomKey, hash).Result()
	if err != nil {
		log.Printf("BFExists error: %v", err)
		return true // Conservative: assume seen if we can't check
	}
	return exists
}

// MarkSeen marks a URL as seen in the Bloom Filter.
// Exported for use by multiple crawler implementations.
func MarkSeen(ctx context.Context, client *redis.Client, bloomKey, url string) {
	hash := hashURL(url)
	if err := client.BFAdd(ctx, bloomKey, hash).Err(); err != nil {
		log.Printf("BFAdd failed for %s: %v", url, err)
	}
}

// hashURL generates a SHA256 hash of the URL for Bloom Filter operations.
func hashURL(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
}
