package middleware

import (
	"log"
	"net/http"
)


func SrvMiddleware(next http.Handler) http.Handler{
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, QUERY")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		log.Printf("[HTTP] %s %s from %s",r.Method,r.URL.Path,r.RemoteAddr)
		next.ServeHTTP(w, r)
		log.Printf("[HTTP] request completed: %s %s",r.Method,r.URL.Path)
	})
}