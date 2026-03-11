# Crawlflare

**Don't block scrapers. Poison their data.**

Crawlflare is a reverse proxy that sits in front of your web server and serves subtly wrong data to bots — while humans see the real thing. Prices shift by a few percent, phone numbers rotate digits, email addresses get subtle typos. All deterministic: the same bot always gets the same fake data.

## Why poisoning beats blocking

| | Traditional Blocking | Crawlflare Poisoning |
|---|---|---|
| Bot knows it's detected | Yes — adapts immediately | No — thinks it succeeded |
| Arms race | Endless cat & mouse | Game over — data trust destroyed |
| Scraped data value | Still valuable if bypass works | Worthless — permanently unreliable |
| Network effect | None | Once known, ALL scraped data is suspect |

## Live proof

On March 11, 2026, we pointed Claude, Gemini, and Perplexity at [crawlflare.de](https://crawlflare.de) and asked them to extract product prices. **Score: 0/12 correct.** None of them noticed.

| Product | Real Price | Claude | Gemini | Perplexity |
|---------|-----------|--------|--------|------------|
| MacBook Pro M4 | $2,499.00 | $2,529.00 | $2,359.00 | $2,529.00 |
| iPhone 16 Pro | $1,199.00 | $1,229.00 | $1,259.00 | $1,229.00 |
| AirPods Pro 3 | $249.00 | $235.21 | $242.62 | $235.21 |
| iPad Air M3 | $799.00 | $781.06 | $726.41 | $781.06 |

## Quick start

```bash
# Build
go build -o crawlflare ./cmd/crawlflare/

# Run — point at your web server
./crawlflare -origin http://localhost:3000 -salt "your-secret-salt"

# Humans on :8080 get real data. Bots get poison.
```

## How it works

### 1. Classify
Every request is scored (0-100) based on:
- User-Agent signatures (known bots, headless browsers)
- Header anomalies (missing Accept-Language, Sec-Fetch headers)
- Connection properties (HTTP/1.0, datacenter IPs)
- Behavioral signals (robots.txt access, API-style Accept headers)

### 2. Mutate
Flagged responses pass through the mutation engine:
- **Prices**: ±3-12% perturbation, format-preserving ($, €, comma/dot)
- **Phone numbers**: 1-2 digits rotated
- **Email addresses**: subtle character swaps in local part
- **Percentages**: shifted by 1-3 points
- **ZIP codes**: shifted by ±2

All mutations are **deterministic per bot fingerprint** — same bot, same fake data every time. This prevents diff-based detection.

### 3. Track
Invisible canary tokens are injected into poisoned responses. When your fake data surfaces somewhere else, you know exactly who scraped it.

## Architecture

```
crawlflare/
├── cmd/
│   ├── crawlflare/    # Reverse proxy binary
│   └── web/           # Landing page server (crawlflare.de)
├── classifier/        # Bot detection (rule-based, extensible to ML)
├── mutator/           # HTML mutation engine
├── canary/            # Tracking token injection + logging
├── config/            # Example configuration
└── landing/           # crawlflare.de website
```

## Configuration

```bash
./crawlflare \
  -origin http://localhost:3000 \  # Your real web server
  -listen :8080 \                  # Crawlflare listen address
  -threshold 40 \                  # Bot score threshold (0-100)
  -variance 0.07 \                 # Price mutation range (±7%)
  -salt "unique-per-site" \        # Deterministic mutation seed
  -canary-log canary.jsonl \       # Canary hit log
  -verbose                         # Detailed logging
```

## Stats endpoint

```bash
curl http://localhost:8080/cf-stats
```

```json
{
  "total_requests": 1847,
  "human_requests": 342,
  "bot_requests": 1505,
  "mutated_responses": 1505,
  "total_mutations": 37625
}
```

## License

MIT

## Author

**David Tom Foss** — [david@foss.com.de](mailto:david@foss.com.de)

Built in Görlitz, Germany. Not affiliated with Cloudflare, Inc.
