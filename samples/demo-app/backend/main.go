package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	port := flag.String("port", "8080", "HTTP port")
	staticDir := flag.String("static-dir", "", "Directory to serve static files from (optional)")
	flag.Parse()

	mux := http.NewServeMux()

	// API health check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Optional static file server for same-machine deployment
	if *staticDir != "" {
		abs, err := filepath.Abs(*staticDir)
		if err != nil {
			log.Fatalf("static-dir resolve: %v", err)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			log.Printf("warning: static-dir %q not found or not a directory, skipping", abs)
		} else {
			mux.Handle("/", http.FileServer(http.Dir(abs)))
		}
	}

	addr := ":" + *port
	log.Printf("demo-app listening on %s (static-dir=%q)", addr, *staticDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
