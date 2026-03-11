package proxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crawlflare/crawlflare/canary"
	"github.com/crawlflare/crawlflare/classifier"
	"github.com/crawlflare/crawlflare/mutator"
)

// Stats tracks proxy-level statistics.
type Stats struct {
	TotalRequests   atomic.Int64
	HumanRequests   atomic.Int64
	BotRequests     atomic.Int64
	MutatedResponses atomic.Int64
	TotalMutations  atomic.Int64
}

// Server is the Crawlflare reverse proxy.
type Server struct {
	Origin     *url.URL
	Classifier *classifier.Classifier
	MutatorCfg mutator.Config
	Canary     *canary.Tracker
	Stats      *Stats
	Logger     *log.Logger
}

func New(originURL string, botThreshold int, mutCfg mutator.Config, canaryTracker *canary.Tracker, logger *log.Logger) (*Server, error) {
	origin, err := url.Parse(originURL)
	if err != nil {
		return nil, fmt.Errorf("invalid origin URL: %w", err)
	}

	return &Server{
		Origin:     origin,
		Classifier: classifier.New(botThreshold),
		MutatorCfg: mutCfg,
		Canary:     canaryTracker,
		Stats:      &Stats{},
		Logger:     logger,
	}, nil
}

// Handler returns the main HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Canary endpoint
	mux.HandleFunc("/cf-canary/", s.Canary.Handler())

	// Stats endpoint (protected, simple auth via query param)
	mux.HandleFunc("/cf-stats", s.statsHandler)

	// Everything else goes through the proxy
	mux.HandleFunc("/", s.proxyHandler)

	return mux
}

func (s *Server) proxyHandler(w http.ResponseWriter, r *http.Request) {
	s.Stats.TotalRequests.Add(1)
	start := time.Now()

	// Classify the request
	score := s.Classifier.Classify(r)
	isBot := s.Classifier.IsBot(score)

	if isBot {
		s.Stats.BotRequests.Add(1)
		s.Logger.Printf("[BOT] score=%d fp=%s reasons=%v ip=%s ua=%q path=%s",
			score.Total, score.Fingerprint[:12], score.Reasons, r.RemoteAddr, r.UserAgent(), r.URL.Path)
	} else {
		s.Stats.HumanRequests.Add(1)
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(s.Origin)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = s.Origin.Scheme
		req.URL.Host = s.Origin.Host
		req.Host = s.Origin.Host
		req.Header.Set("X-Forwarded-For", r.RemoteAddr)
		req.Header.Set("X-Forwarded-Proto", r.URL.Scheme)
	}

	if isBot {
		// Intercept and mutate the response
		proxy.ModifyResponse = func(resp *http.Response) error {
			return s.mutateResponse(resp, score)
		}
	}

	proxy.ServeHTTP(w, r)

	s.Logger.Printf("[%s] %d %s %s %dms bot=%v score=%d",
		r.Method, 200, r.URL.Path, r.RemoteAddr,
		time.Since(start).Milliseconds(), isBot, score.Total)
}

func (s *Server) mutateResponse(resp *http.Response, score classifier.Score) error {
	contentType := resp.Header.Get("Content-Type")

	// Only mutate HTML responses
	if !strings.Contains(contentType, "text/html") {
		return nil
	}

	// Read the body
	var body []byte
	var err error

	// Handle gzipped responses
	isGzipped := resp.Header.Get("Content-Encoding") == "gzip"
	if isGzipped {
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil // fail open
		}
		body, err = io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return nil
		}
	} else {
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil
		}
	}
	resp.Body.Close()

	// Create mutation engine with deterministic seed
	seed := classifier.FingerprintToSeed(score.Fingerprint, s.MutatorCfg.SiteSalt)
	engine := mutator.New(s.MutatorCfg, seed)

	// Mutate
	mutatedBody, stats := engine.MutateHTML(body)

	if stats.Total() > 0 {
		s.Stats.MutatedResponses.Add(1)
		s.Stats.TotalMutations.Add(int64(stats.Total()))
		s.Logger.Printf("[MUTATE] fp=%s prices=%d phones=%d emails=%d numbers=%d canaries=%d",
			score.Fingerprint[:12], stats.PricesMutated, stats.PhonesMutated,
			stats.EmailsMutated, stats.NumbersMutated, stats.CanariesPlaced)
	}

	// Re-encode if was gzipped
	if isGzipped {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write(mutatedBody)
		gz.Close()
		mutatedBody = buf.Bytes()
	} else {
		// Remove Content-Length since we changed the body
		resp.Header.Del("Content-Length")
	}

	resp.Body = io.NopCloser(bytes.NewReader(mutatedBody))
	resp.ContentLength = int64(len(mutatedBody))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(mutatedBody)))

	return nil
}

func (s *Server) statsHandler(w http.ResponseWriter, r *http.Request) {
	// Simple stats endpoint
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{
  "total_requests": %d,
  "human_requests": %d,
  "bot_requests": %d,
  "mutated_responses": %d,
  "total_mutations": %d,
  "canary": %s
}`,
		s.Stats.TotalRequests.Load(),
		s.Stats.HumanRequests.Load(),
		s.Stats.BotRequests.Load(),
		s.Stats.MutatedResponses.Load(),
		s.Stats.TotalMutations.Load(),
		formatCanaryStats(s.Canary.Stats()),
	)
}

func formatCanaryStats(stats map[string]int) string {
	return fmt.Sprintf(`{"total_hits": %d, "unique_ips": %d, "unique_tokens": %d}`,
		stats["total_hits"], stats["unique_ips"], stats["unique_tokens"])
}
