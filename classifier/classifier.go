package classifier

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Score represents a bot probability score from 0 (definitely human) to 100 (definitely bot).
type Score struct {
	Total      int
	Reasons    []string
	Fingerprint string // deterministic fingerprint for mutation seeding
}

// Classifier scores incoming requests for bot probability.
type Classifier struct {
	Threshold    int
	KnownBotUAs []string
	mu           sync.RWMutex
}

func New(threshold int) *Classifier {
	return &Classifier{
		Threshold: threshold,
		KnownBotUAs: []string{
			"googlebot", "bingbot", "yandexbot", "baiduspider",
			"facebookexternalhit", "twitterbot", "rogerbot", "linkedinbot",
			"embedly", "slurp", "duckduckbot", "semrushbot",
			"ahrefsbot", "mj12bot", "dotbot", "petalbot",
			"bytespider", "gptbot", "chatgpt-user", "claudebot",
			"anthropic-ai", "cohere-ai", "ccbot", "scrapy",
			"python-requests", "python-urllib", "go-http-client",
			"java/", "curl/", "wget/", "httpie/",
			"node-fetch", "axios/", "undici/",
			"headlesschrome", "phantomjs", "selenium",
			"puppeteer", "playwright",
			"crawl", "spider", "bot/", "crawler",
		},
	}
}

// Classify returns a bot score for the request.
func (c *Classifier) Classify(r *http.Request) Score {
	score := Score{}
	var points int

	fp := c.buildFingerprint(r)
	score.Fingerprint = fp

	// 1. User-Agent analysis
	ua := strings.ToLower(r.UserAgent())
	if ua == "" {
		points += 30
		score.Reasons = append(score.Reasons, "empty-ua")
	} else {
		for _, botUA := range c.KnownBotUAs {
			if strings.Contains(ua, botUA) {
				points += 40
				score.Reasons = append(score.Reasons, "known-bot-ua:"+botUA)
				break
			}
		}
	}

	// 2. Header ordering and presence anomalies
	points += c.scoreHeaders(r, &score)

	// 3. Connection properties
	points += c.scoreConnection(r, &score)

	// 4. Behavioral signals from single request
	points += c.scoreBehavior(r, &score)

	score.Total = min(points, 100)
	return score
}

// IsBot returns true if the score exceeds the threshold.
func (c *Classifier) IsBot(s Score) bool {
	return s.Total >= c.Threshold
}

func (c *Classifier) scoreHeaders(r *http.Request, s *Score) int {
	points := 0

	// Real browsers always send Accept
	if r.Header.Get("Accept") == "" {
		points += 15
		s.Reasons = append(s.Reasons, "no-accept")
	}

	// Real browsers send Accept-Language
	if r.Header.Get("Accept-Language") == "" {
		points += 15
		s.Reasons = append(s.Reasons, "no-accept-language")
	}

	// Real browsers send Accept-Encoding
	if r.Header.Get("Accept-Encoding") == "" {
		points += 10
		s.Reasons = append(s.Reasons, "no-accept-encoding")
	}

	// Check for suspicious header combinations
	// Browsers that claim to be Chrome/Firefox but lack Sec-Fetch headers
	if (strings.Contains(strings.ToLower(r.UserAgent()), "chrome") ||
		strings.Contains(strings.ToLower(r.UserAgent()), "firefox")) &&
		r.Header.Get("Sec-Fetch-Mode") == "" {
		points += 20
		s.Reasons = append(s.Reasons, "missing-sec-fetch")
	}

	// Connection header present in HTTP/2+ is suspicious (browsers don't send it)
	if r.ProtoMajor >= 2 && r.Header.Get("Connection") != "" {
		points += 10
		s.Reasons = append(s.Reasons, "connection-header-in-h2")
	}

	return points
}

func (c *Classifier) scoreConnection(r *http.Request, s *Score) int {
	points := 0

	// HTTP/1.0 is very suspicious in 2026
	if r.ProtoMajor == 1 && r.ProtoMinor == 0 {
		points += 15
		s.Reasons = append(s.Reasons, "http10")
	}

	// Check if coming from known datacenter IP ranges
	ip := extractIP(r)
	if isDatacenterIP(ip) {
		points += 20
		s.Reasons = append(s.Reasons, "datacenter-ip")
	}

	return points
}

func (c *Classifier) scoreBehavior(r *http.Request, s *Score) int {
	points := 0

	// Requesting robots.txt is a strong bot signal
	if r.URL.Path == "/robots.txt" {
		points += 5
		s.Reasons = append(s.Reasons, "robots-txt")
	}

	// API-style Accept headers
	accept := r.Header.Get("Accept")
	if accept == "application/json" || accept == "*/*" {
		points += 5
		s.Reasons = append(s.Reasons, "api-accept")
	}

	// No Referer on internal pages (not homepage)
	if r.URL.Path != "/" && r.Header.Get("Referer") == "" {
		points += 5
		s.Reasons = append(s.Reasons, "no-referer-internal")
	}

	return points
}

// buildFingerprint creates a deterministic fingerprint from request properties.
// This is used to seed the mutation engine so the same bot always gets the same fake data.
func (c *Classifier) buildFingerprint(r *http.Request) string {
	h := sha256.New()

	// IP
	ip := extractIP(r)
	h.Write([]byte(ip))

	// User-Agent
	h.Write([]byte(r.UserAgent()))

	// Accept-Language (locale fingerprint)
	h.Write([]byte(r.Header.Get("Accept-Language")))

	// Header order (serialize header keys in order received)
	for _, key := range headerKeys(r) {
		h.Write([]byte(key))
	}

	// Protocol version
	h.Write([]byte(r.Proto))

	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:16])
}

func extractIP(r *http.Request) string {
	// Check X-Forwarded-For first
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func headerKeys(r *http.Request) []string {
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	return keys
}

// isDatacenterIP checks if an IP belongs to known cloud/datacenter ranges.
// This is a simplified version — production would use a MaxMind or ipinfo.io database.
func isDatacenterIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// Major cloud provider ranges (simplified)
	datacenterRanges := []string{
		// AWS
		"3.0.0.0/8", "13.0.0.0/8", "18.0.0.0/8", "35.0.0.0/8",
		"52.0.0.0/8", "54.0.0.0/8",
		// GCP
		"34.0.0.0/8", "35.0.0.0/8",
		// Azure
		"20.0.0.0/8", "40.0.0.0/8", "51.0.0.0/8", "52.0.0.0/8",
		// DigitalOcean
		"104.131.0.0/16", "159.65.0.0/16", "167.71.0.0/16",
		// Hetzner
		"95.216.0.0/16", "135.181.0.0/16", "65.108.0.0/16",
		// OVH
		"51.38.0.0/16", "51.68.0.0/16", "51.75.0.0/16", "51.77.0.0/16",
		// Vultr
		"45.32.0.0/16", "45.63.0.0/16", "108.61.0.0/16",
	}

	for _, cidrStr := range datacenterRanges {
		_, cidr, err := net.ParseCIDR(cidrStr)
		if err != nil {
			continue
		}
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// fingerprintToSeed converts a fingerprint string to a uint64 seed for deterministic mutation.
func FingerprintToSeed(fingerprint string, siteSalt string) uint64 {
	h := sha256.New()
	h.Write([]byte(siteSalt))
	h.Write([]byte(fingerprint))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

