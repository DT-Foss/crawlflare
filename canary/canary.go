package canary

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Hit records a canary token being triggered.
type Hit struct {
	TokenID     string    `json:"token_id"`
	IP          string    `json:"ip"`
	UserAgent   string    `json:"user_agent"`
	Referer     string    `json:"referer"`
	Timestamp   time.Time `json:"timestamp"`
	RequestPath string    `json:"request_path"`
}

// Tracker logs canary token hits.
type Tracker struct {
	mu       sync.Mutex
	hits     []Hit
	logFile  string
	callback func(Hit) // optional callback for real-time alerting
}

func New(logFile string) *Tracker {
	return &Tracker{
		logFile: logFile,
		hits:    make([]Hit, 0),
	}
}

func (t *Tracker) SetCallback(cb func(Hit)) {
	t.callback = cb
}

// Record logs a canary hit.
func (t *Tracker) Record(h Hit) {
	t.mu.Lock()
	t.hits = append(t.hits, h)
	t.mu.Unlock()

	// Append to log file
	if t.logFile != "" {
		data, _ := json.Marshal(h)
		f, err := os.OpenFile(t.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			f.Write(append(data, '\n'))
			f.Close()
		}
	}

	if t.callback != nil {
		t.callback(h)
	}
}

// Handler returns an HTTP handler for canary endpoints.
func (t *Tracker) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hit := Hit{
			TokenID:     extractTokenID(r.URL.Path),
			IP:          r.RemoteAddr,
			UserAgent:   r.UserAgent(),
			Referer:     r.Header.Get("Referer"),
			Timestamp:   time.Now().UTC(),
			RequestPath: r.URL.Path,
		}
		t.Record(hit)

		// Return a plausible-looking empty page
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		fmt.Fprint(w, "<!DOCTYPE html><html><head><title>Page Not Found</title></head><body><h1>404</h1></body></html>")
	}
}

// Stats returns canary hit statistics.
func (t *Tracker) Stats() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats := map[string]int{
		"total_hits":  len(t.hits),
		"unique_ips":  0,
		"unique_tokens": 0,
	}

	ips := make(map[string]bool)
	tokens := make(map[string]bool)
	for _, h := range t.hits {
		ips[h.IP] = true
		tokens[h.TokenID] = true
	}
	stats["unique_ips"] = len(ips)
	stats["unique_tokens"] = len(tokens)
	return stats
}

func extractTokenID(path string) string {
	// /cf-canary/TOKEN_ID
	parts := splitPath(path)
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return path
}

func splitPath(path string) []string {
	var parts []string
	for _, p := range splitString(path, '/') {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func splitString(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}
