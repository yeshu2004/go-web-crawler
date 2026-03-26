package db

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

func InitializeBloomFilter(ctx context.Context, client *redis.Client, bloomKey string, errorRate float64, capacity int64) error {
	res, err := client.Exists(ctx, bloomKey).Result()
	if err != nil {
		return fmt.Errorf("failed to check bloom key: %w", err)
	}

	if res == 0 {
		if err := client.BFReserve(ctx, bloomKey, errorRate, capacity).Err(); err != nil {
			return fmt.Errorf("failed to create bloom filter: %w", err)
		}
		fmt.Printf("succesfully initalized bloom filter: %v\n", bloomKey)
	}
	if res == 1 {
		log.Println("clearing old Bloom filter...")
		if err := client.Del(ctx, bloomKey).Err(); err != nil {
			return fmt.Errorf("failed to delete old bloom filter: %w", err)
		}
		time.Sleep(time.Millisecond * 1)
		if err := client.BFReserve(ctx, bloomKey, errorRate, capacity).Err(); err != nil {
			return fmt.Errorf("failed to recreate bloom filter: %w", err)
		}
		fmt.Printf("succesfully initalized bloom filter: %v\n", bloomKey)

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
		log.Printf("Failed to connect to Redis: %v. Ensure Redis is running on localhost:6379.", err)
		return nil, err
	}
	log.Println("Redis connection successful")
	return Client, nil
}
