package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"

	NATS "github/yeshu2004/go-epics/nats"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown on Ctrl+C
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		log.Println("interrupt — shutting down...")
		cancel()
	}()

	nc, err := NATS.NewNATSConn()
	if err != nil {
		log.Fatal("nats connect:", err)
	}

	if err := nc.CreateTupleStream(ctx); err != nil {
		log.Fatal("create stream:", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < NATS.NumPartitions; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if err := nc.StartConsumer(ctx, id); err != nil {
				log.Printf("[consumer-%d] exited with error: %v", id, err)
			}
		}(i)
	}

	wg.Wait()
	log.Println("all consumers stopped")
}