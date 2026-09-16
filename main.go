package main

import (
	"context"
	"github/yeshu2004/go-epics/consumer"
	"github/yeshu2004/go-epics/middleware"
	"github/yeshu2004/go-epics/router"
	"log"
	"net/http"
)


func main() {
	PORT := ":8000"
	ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
	
	routerSrv, err := router.ConnectInfra();
	if err != nil{
		log.Fatalln(err);
	}
	go consumer.Consume(ctx)
	
	mux := routerSrv.Router()
	srv := &http.Server{
		Addr: PORT,
		Handler: middleware.SrvMiddleware(mux),
	}

	log.Printf("server starting on port%v\n", PORT)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server running error: %v\n", err)
	}
}
