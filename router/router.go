package router

import (
	"context"
	"encoding/json"
	"fmt"
	c "github/yeshu2004/go-epics/crawler"
	"github/yeshu2004/go-epics/db"
	"github/yeshu2004/go-epics/nats"

	"log"
	"net/http"

	"github.com/dgraph-io/badger/v4"
	"github.com/redis/go-redis/v9"
)

var crawlerManager = NewCrawlerManager()

type RouterSrv struct{
	rdb *redis.Client
	badger *badger.DB
	nats   *nats.Client
}

func (s *RouterSrv) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", defaultHandler)
	mux.HandleFunc("/run/crawler", s.runWebCrawler)
	mux.HandleFunc("/shutdown/crawler", shutdownCrawler)

	return mux
}

func ConnectInfra() (*RouterSrv, error){
	ctx := context.Background()
	rdb, err := db.RedisInit(ctx)
	if err != nil {
		log.Println("Redis connection failed:", err)
		return nil, err
	}

	badgerPath := "./crwal_db/"
	badgerDB, err := badger.Open(badger.LSMOnlyOptions(badgerPath))
	if err != nil {
		rdb.Close();
		log.Println("BadgerDB connection failed:", err)
		return nil, err
	}

	// NATS AND PG connection
	natsClient, err := nats.NewNATSANDPGConn()
	if err != nil {
		rdb.Close()
        badgerDB.Close()
		log.Println("Nats && PG connection failed:", err)
		return nil, err
	}

	if err := natsClient.CreateTupleStream(ctx); err != nil {
		rdb.Close()
        badgerDB.Close()
		log.Println(err)
		return nil, err
	}
	log.Println("Nats Tuple Stream connection sucessfull...")

	return &RouterSrv{
		rdb: rdb,
		badger: badgerDB,
		nats: natsClient,
	}, nil
}

// every request starts a new independent crawler execution.
// Infrastructure (Redis, NATS, PostgreSQL, Badger) is shared,
// while crawler-specific state such as ID, queue and Bloom filter
// is isolated per crawler.
func (s *RouterSrv) runWebCrawler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		err := fmt.Sprintf("expected: %v, recived: %v", http.MethodPost, r.Method)
		writeError(w, http.StatusMethodNotAllowed, err)
		return
	}

	type req struct {
		InitalUrlSeeds []string `json:"seed_url"`
	}
	var reqBody req
	
	// the url of the main seed will come through the body
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	crawler, err := c.NewCrawler(ctx, s.rdb, s.badger, s.nats)
	if err != nil {
		cancel();
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	crawlerManager.Add(crawler.Id, cancel)

	go func(ctx context.Context, urls []string) {
		defer crawlerManager.Remove(crawler.Id)
		defer cancel()
		crawler.Run(ctx, urls)
	}(ctx, reqBody.InitalUrlSeeds)

	writeResponse(w, http.StatusAccepted, map[string]string{
		"id":     crawler.Id,
		"status": "started",
	})
}

func shutdownCrawler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		err := fmt.Sprintf("expected: %v, received: %v", http.MethodGet, r.Method)
		writeError(w, http.StatusMethodNotAllowed, err)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "crawler id is required")
		return
	}

	if !crawlerManager.Stop(id) {
		writeError(w, http.StatusNotFound, "crawler not running")
		return
	}

	writeResponse(w, http.StatusOK, map[string]string{
		"id":     id,
		"status": "shutdown requested",
	})
}

func defaultHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		err := fmt.Sprintf("expected: %v, recived: %v", http.MethodGet, r.Method)
		writeError(w, http.StatusMethodNotAllowed, err)
		return
	}

	writeResponse(w, http.StatusOK, "working...")
}

func writeResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := Response{
		Data: data,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func writeError(rw http.ResponseWriter, status int, errorMessage string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)

	log.Printf("[ERROR]: %v", errorMessage)

	err := ErrorResponse{Error: errorMessage}
	_ = json.NewEncoder(rw).Encode(err)
}

type Response struct {
	Data any `json:"data"`
}
type ErrorResponse struct {
	Error string `json:"error"`
}
