package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/crawlflare/crawlflare/canary"
	"github.com/crawlflare/crawlflare/classifier"
	"github.com/crawlflare/crawlflare/mutator"
)

// LiveStats tracks stats for the landing page demo.
type LiveStats struct {
	TotalRequests    atomic.Int64
	HumanRequests    atomic.Int64
	BotRequests      atomic.Int64
	MutatedResponses atomic.Int64
	TotalMutations   atomic.Int64
}

var indexHTML []byte

func main() {
	listen := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		listen = ":" + p
	}
	salt := os.Getenv("CRAWLFLARE_SALT")
	if salt == "" {
		salt = "crawlflare-demo-2026"
	}

	// Read landing page
	htmlPath := "landing/index.html"
	if p := os.Getenv("HTML_PATH"); p != "" {
		htmlPath = p
	}
	var err error
	indexHTML, err = os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("Cannot read %s: %v", htmlPath, err)
	}

	logger := log.New(os.Stdout, "[crawlflare-web] ", log.LstdFlags)
	stats := &LiveStats{}
	cls := classifier.New(35)
	tracker := canary.New("canary_hits.jsonl")

	mutCfg := mutator.DefaultConfig()
	mutCfg.SiteSalt = salt
	mutCfg.InjectCanary = true

	mux := http.NewServeMux()

	// Canary trap
	mux.HandleFunc("/cf-canary/", tracker.Handler())

	// Live stats API
	mux.HandleFunc("/cf-stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]int64{
			"total_requests":    stats.TotalRequests.Load(),
			"human_requests":    stats.HumanRequests.Load(),
			"bot_requests":      stats.BotRequests.Load(),
			"mutated_responses": stats.MutatedResponses.Load(),
			"total_mutations":   stats.TotalMutations.Load(),
		})
	})

	// Main handler — the demo IS the product
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		stats.TotalRequests.Add(1)

		score := cls.Classify(r)
		isBot := cls.IsBot(score)

		if isBot {
			stats.BotRequests.Add(1)
			logger.Printf("[BOT] score=%d fp=%s reasons=%v ip=%s ua=%q",
				score.Total, score.Fingerprint[:12], score.Reasons, r.RemoteAddr, r.UserAgent())

			seed := classifier.FingerprintToSeed(score.Fingerprint, salt)
			engine := mutator.New(mutCfg, seed)
			mutated, mstats := engine.MutateHTML(indexHTML)

			if mstats.Total() > 0 {
				stats.MutatedResponses.Add(1)
				stats.TotalMutations.Add(int64(mstats.Total()))
				logger.Printf("[MUTATE] prices=%d phones=%d emails=%d numbers=%d canaries=%d",
					mstats.PricesMutated, mstats.PhonesMutated, mstats.EmailsMutated,
					mstats.NumbersMutated, mstats.CanariesPlaced)
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, "index.html", time.Now(), bytes.NewReader(mutated))
		} else {
			stats.HumanRequests.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=60")
			http.ServeContent(w, r, "index.html", time.Now(), bytes.NewReader(indexHTML))
		}
	})

	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Println("│         CRAWLFLARE WEB v0.1.0               │")
	fmt.Println("│    Landing Page + Live Poisoning Demo        │")
	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Printf("│  Listen:    %-31s │\n", listen)
	fmt.Printf("│  Salt:      %-31s │\n", salt[:min(len(salt), 20)]+"...")
	fmt.Println("│  Threshold: 35                               │")
	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Println("│  Humans  → real prices & data                │")
	fmt.Println("│  Bots    → poisoned prices + canary tokens   │")
	fmt.Println("│  Stats   → /cf-stats (live JSON)             │")
	fmt.Println("│  Canary  → /cf-canary/* (trap endpoints)     │")
	fmt.Println("└─────────────────────────────────────────────┘")

	server := &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logger.Printf("Starting on %s", listen)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
