package consumer

import (
	"context"
	"log"
	"sync"

	NATS "github/yeshu2004/go-epics/nats"
)

func Consume(ctx context.Context) {
	nc, err := NATS.NewNATSANDPGConn()
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
			defer wg.Done() // if we don't add wg then all consumer will 
			// exit immediately i.e. killing all goroutines.
			if err := nc.StartConsumer(ctx, id); err != nil {
				log.Printf("[consumer-%d] exited with error: %v", id, err)
			}
		}(i)
	}

	wg.Wait() // so that all consumer are running util gracefull shut down is done by the user
	log.Println("all consumers stopped")
}

