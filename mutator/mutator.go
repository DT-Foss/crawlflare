package mutator

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Config controls mutation behavior per site.
type Config struct {
	// PriceVariance is the max percentage to shift prices (e.g., 0.05 = ±5%)
	PriceVariance float64
	// PhoneDigitRotation shifts phone digits by this amount
	PhoneDigitRotation int
	// ZipCodeShift shifts postal codes by this amount
	ZipCodeShift int
	// InjectCanary adds invisible tracking tokens
	InjectCanary bool
	// CanaryEndpoint is the URL canary tokens phone home to
	CanaryEndpoint string
	// MutateEmails slightly alters email addresses
	MutateEmails bool
	// SiteSalt is the per-site secret used for deterministic seeding
	SiteSalt string
}

func DefaultConfig() Config {
	return Config{
		PriceVariance:      0.07, // ±7%
		PhoneDigitRotation: 1,
		ZipCodeShift:       2,
		InjectCanary:       true,
		MutateEmails:       true,
		SiteSalt:           "change-me-per-site",
	}
}

// Engine mutates HTML content deterministically based on a seed.
type Engine struct {
	cfg Config
	rng *rand.Rand
}

func New(cfg Config, seed uint64) *Engine {
	return &Engine{
		cfg: cfg,
		rng: rand.New(rand.NewSource(int64(seed))),
	}
}

// MutateHTML parses HTML, mutates text nodes, and returns the modified HTML.
func (e *Engine) MutateHTML(body []byte) ([]byte, *MutationStats) {
	stats := &MutationStats{}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		// If we can't parse, return original (fail open for humans, fail safe for bots)
		return body, stats
	}

	e.walkAndMutate(doc, stats)

	// Inject canary if configured
	if e.cfg.InjectCanary {
		e.injectCanaryTokens(doc, stats)
	}

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return body, stats
	}

	return buf.Bytes(), stats
}

// MutationStats tracks what was mutated for logging/dashboard.
type MutationStats struct {
	PricesMutated  int
	PhonesMutated  int
	EmailsMutated  int
	NumbersMutated int
	CanariesPlaced int
}

func (s *MutationStats) Total() int {
	return s.PricesMutated + s.PhonesMutated + s.EmailsMutated + s.NumbersMutated + s.CanariesPlaced
}

// Regex patterns for detecting mutable content.
var (
	// Prices: $12.99, €49,90, £100.00, 12,99 €, etc.
	priceRe = regexp.MustCompile(`(?i)([$€£¥])\s*(\d{1,7}[.,]\d{2})|((\d{1,7})[.,](\d{2}))\s*(?:[$€£¥]|EUR|USD|GBP)`)
	// Standalone prices with common patterns
	priceStandaloneRe = regexp.MustCompile(`(\d{1,7})[.,](\d{2})\s*€|\$\s*(\d{1,7})[.,](\d{2})`)
	// Phone numbers: various formats
	phoneRe = regexp.MustCompile(`(?:\+?\d{1,3}[-.\s]?)?\(?\d{2,4}\)?[-.\s]?\d{3,4}[-.\s]?\d{3,4}`)
	// Email addresses
	emailRe = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	// Generic numbers that look like data (percentages, quantities, etc.)
	numberRe = regexp.MustCompile(`\b(\d{1,3}(?:[.,]\d{1,2})?)\s*%`)
	// ZIP/postal codes (5 digit)
	zipRe = regexp.MustCompile(`\b(\d{5})\b`)
)

func (e *Engine) walkAndMutate(n *html.Node, stats *MutationStats) {
	if n.Type == html.TextNode {
		// Skip script and style content
		if n.Parent != nil && (n.Parent.Data == "script" || n.Parent.Data == "style") {
			return
		}
		n.Data = e.mutateText(n.Data, stats)
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		e.walkAndMutate(c, stats)
	}
}

func (e *Engine) mutateText(text string, stats *MutationStats) string {
	// Mutate prices
	text = priceRe.ReplaceAllStringFunc(text, func(match string) string {
		return e.mutatePrice(match, stats)
	})
	text = priceStandaloneRe.ReplaceAllStringFunc(text, func(match string) string {
		return e.mutatePrice(match, stats)
	})

	// Mutate phone numbers
	text = phoneRe.ReplaceAllStringFunc(text, func(match string) string {
		return e.mutatePhone(match, stats)
	})

	// Mutate emails
	if e.cfg.MutateEmails {
		text = emailRe.ReplaceAllStringFunc(text, func(match string) string {
			return e.mutateEmail(match, stats)
		})
	}

	// Mutate percentages
	text = numberRe.ReplaceAllStringFunc(text, func(match string) string {
		return e.mutatePercentage(match, stats)
	})

	// Mutate ZIP codes
	text = zipRe.ReplaceAllStringFunc(text, func(match string) string {
		return e.mutateZip(match, stats)
	})

	return text
}

func (e *Engine) mutatePrice(match string, stats *MutationStats) string {
	// Extract the numeric part
	numStr := extractNumber(match)
	if numStr == "" {
		return match
	}

	val, err := strconv.ParseFloat(strings.Replace(numStr, ",", ".", 1), 64)
	if err != nil || val == 0 {
		return match
	}

	// Apply deterministic perturbation within configured variance
	perturbation := (e.rng.Float64()*2 - 1) * e.cfg.PriceVariance
	newVal := val * (1 + perturbation)

	// Round to 2 decimal places
	newVal = math.Round(newVal*100) / 100

	// Preserve original format (comma vs dot)
	var newStr string
	if strings.Contains(numStr, ",") {
		newStr = strings.Replace(fmt.Sprintf("%.2f", newVal), ".", ",", 1)
	} else {
		newStr = fmt.Sprintf("%.2f", newVal)
	}

	stats.PricesMutated++
	return strings.Replace(match, numStr, newStr, 1)
}

func (e *Engine) mutatePhone(match string, stats *MutationStats) string {
	// Rotate one or two digits deterministically
	digits := []byte(match)
	changed := false
	rotations := 0

	for i := range digits {
		if digits[i] >= '0' && digits[i] <= '9' && rotations < 2 {
			// Pick a position to rotate based on RNG
			if e.rng.Float64() < 0.3 { // 30% chance per digit, max 2
				shift := byte(e.cfg.PhoneDigitRotation)
				digits[i] = '0' + (digits[i]-'0'+shift)%10
				changed = true
				rotations++
			}
		}
	}

	if changed {
		stats.PhonesMutated++
	}
	return string(digits)
}

func (e *Engine) mutateEmail(match string, stats *MutationStats) string {
	parts := strings.SplitN(match, "@", 2)
	if len(parts) != 2 {
		return match
	}

	local := parts[0]
	if len(local) < 3 {
		return match
	}

	// Insert or swap a character in the local part
	pos := e.rng.Intn(len(local)-1) + 1 // never first char
	chars := []byte(local)

	// Swap with neighbor or shift by 1
	if pos < len(chars)-1 {
		chars[pos], chars[pos+1] = chars[pos+1], chars[pos]
	} else {
		chars[pos] = chars[pos] + 1
	}

	stats.EmailsMutated++
	return string(chars) + "@" + parts[1]
}

func (e *Engine) mutatePercentage(match string, stats *MutationStats) string {
	submatch := numberRe.FindStringSubmatch(match)
	if len(submatch) < 2 {
		return match
	}

	val, err := strconv.ParseFloat(strings.Replace(submatch[1], ",", ".", 1), 64)
	if err != nil {
		return match
	}

	// Shift by 1-3 percentage points
	shift := (e.rng.Float64()*4 - 2) // ±2 percentage points
	newVal := val + shift
	if newVal < 0 {
		newVal = val + math.Abs(shift)
	}
	if newVal > 100 {
		newVal = val - math.Abs(shift)
	}

	newStr := fmt.Sprintf("%.1f", newVal)
	if !strings.Contains(submatch[1], ".") && !strings.Contains(submatch[1], ",") {
		newStr = fmt.Sprintf("%.0f", newVal)
	}

	stats.NumbersMutated++
	return strings.Replace(match, submatch[1], newStr, 1)
}

func (e *Engine) mutateZip(match string, stats *MutationStats) string {
	val, err := strconv.Atoi(match)
	if err != nil {
		return match
	}

	shift := e.rng.Intn(e.cfg.ZipCodeShift*2+1) - e.cfg.ZipCodeShift
	newVal := val + shift
	if newVal < 10000 {
		newVal = val + e.cfg.ZipCodeShift
	}

	stats.NumbersMutated++
	return fmt.Sprintf("%05d", newVal)
}

func (e *Engine) injectCanaryTokens(doc *html.Node, stats *MutationStats) {
	// Generate a unique canary ID
	canaryID := fmt.Sprintf("cf-%x", e.rng.Uint64())

	// Create an invisible div with a unique tracking pixel
	canaryDiv := &html.Node{
		Type: html.ElementNode,
		Data: "div",
		Attr: []html.Attribute{
			{Key: "style", Val: "position:absolute;left:-9999px;top:-9999px;width:1px;height:1px;overflow:hidden"},
			{Key: "aria-hidden", Val: "true"},
		},
	}

	// Add a link that only crawlers would follow
	canaryLink := &html.Node{
		Type: html.ElementNode,
		Data: "a",
		Attr: []html.Attribute{
			{Key: "href", Val: fmt.Sprintf("/cf-canary/%s", canaryID)},
			{Key: "tabindex", Val: "-1"},
		},
	}
	canaryLink.AppendChild(&html.Node{
		Type: html.TextNode,
		Data: " ",
	})
	canaryDiv.AppendChild(canaryLink)

	// If canary endpoint configured, add an invisible image
	if e.cfg.CanaryEndpoint != "" {
		canaryImg := &html.Node{
			Type: html.ElementNode,
			Data: "img",
			Attr: []html.Attribute{
				{Key: "src", Val: fmt.Sprintf("%s?t=%s", e.cfg.CanaryEndpoint, canaryID)},
				{Key: "width", Val: "1"},
				{Key: "height", Val: "1"},
				{Key: "style", Val: "display:none"},
				{Key: "alt", Val: ""},
			},
		}
		canaryDiv.AppendChild(canaryImg)
	}

	// Find body and append
	body := findNode(doc, "body")
	if body != nil {
		body.AppendChild(canaryDiv)
		stats.CanariesPlaced++
	}
}

func findNode(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findNode(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func extractNumber(s string) string {
	// Find the numeric portion of a price string
	re := regexp.MustCompile(`(\d{1,7}[.,]\d{2})`)
	match := re.FindString(s)
	return match
}
