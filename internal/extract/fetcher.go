// Package extract fetches article pages and turns them into cleaned,
// full-text HTML. Fetching is rate-limited per host; extraction combines
// go-readability with UI-managed per-site CSS-selector rules.
package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rss-scraper/internal/config"
)

// Exported errors.
var (
	// ErrExtract is the generic extraction failure: fetch failed, content was
	// not HTML, or no readable content could be found.
	ErrExtract = errors.New("extract: extraction failed")
	// ErrRateLimited is returned when a host responds 429 (Too Many Requests).
	ErrRateLimited = errors.New("extract: rate limited (HTTP 429)")
	// ErrForbidden is returned when a host responds 403 (Forbidden).
	ErrForbidden = errors.New("extract: forbidden (HTTP 403)")
)

// maxResponseSize caps how much of a response body is read.
const maxResponseSize = 5 << 20 // 5MB

// hostLimiter spaces out fetches to a single host.
type hostLimiter struct {
	mu       sync.Mutex
	nextTime time.Time // earliest moment the next fetch may start
}

// wait blocks until the host may be fetched again, then reserves the slot.
// spacing is the minimum gap between fetches; jitter is added on top.
// jitterFn supplies random int63 samples (injected so the owner's locked RNG
// stays goroutine-safe).
func (h *hostLimiter) wait(spacing, jitter time.Duration, jitterFn func() int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	if h.nextTime.After(now) {
		time.Sleep(h.nextTime.Sub(now))
	}
	gap := spacing
	if jitter > 0 {
		gap += time.Duration(jitterFn() % int64(jitter))
		if gap < spacing {
			gap = spacing
		}
	}
	h.nextTime = time.Now().Add(gap)
}

// Fetcher performs polite, rate-limited HTTP GETs.
type Fetcher struct {
	client  *http.Client
	cfg     *config.Config
	mu      sync.Mutex
	hosts   map[string]*hostLimiter
	rnd     *rand.Rand
	rndMu   sync.Mutex
	perHost int // cap on goroutines per host (0 = unlimited)
}

// NewFetcher builds a Fetcher from configuration: per-request timeout,
// User-Agent, per-host spacing plus jitter.
func NewFetcher(cfg *config.Config) *Fetcher {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Timeout:   cfg.FetchTimeout,
		Transport: transport,
	}
	// Follow redirects by default; http.Client does this out of the box for
	// GET (up to 10 hops).
	return &Fetcher{
		client:  client,
		cfg:     cfg,
		hosts:   make(map[string]*hostLimiter),
		rnd:     rand.New(rand.NewSource(time.Now().UnixNano())),
		perHost: 4,
	}
}

// limiterFor returns (creating if needed) the limiter for a host. Safe for
// concurrent use.
func (f *Fetcher) limiterFor(host string) *hostLimiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	hl, ok := f.hosts[host]
	if !ok {
		hl = &hostLimiter{}
		f.hosts[host] = hl
	}
	return hl
}

// randInt63 returns a jitter sample under a lock (rand.Rand is not
// goroutine-safe).
func (f *Fetcher) randInt63() int64 {
	f.rndMu.Lock()
	defer f.rndMu.Unlock()
	return f.rnd.Int63()
}

// Fetch GETs url, rate-limited per host, and returns the body bytes. Only
// 2xx responses count as success; 429 maps to ErrRateLimited, 403 to
// ErrForbidden, other non-2xx to ErrExtract. Response bodies larger than
// 5MB are truncated-failing with ErrExtract.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: bad url %q", ErrExtract, rawURL)
	}

	// Per-host rate limiting happens before the request so concurrent
	// fetches to the same host are spaced out while different hosts proceed
	// in parallel.
	hl := f.limiterFor(u.Host)
	hl.wait(f.cfg.FetchSpacing, f.cfg.FetchJitter, f.randInt63)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrExtract, err)
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch %s: %v", ErrExtract, rawURL, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// success below
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, rawURL)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s", ErrForbidden, rawURL)
	default:
		return nil, fmt.Errorf("%w: %s: HTTP %d", ErrExtract, rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read body %s: %v", ErrExtract, rawURL, err)
	}
	if len(body) > maxResponseSize {
		return nil, fmt.Errorf("%w: %s: response larger than %d bytes", ErrExtract, rawURL, maxResponseSize)
	}
	return body, nil
}

// contentTypeIsHTML reports whether a Content-Type header points at HTML.
func contentTypeIsHTML(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return true // be lenient when the server omits the header
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "text/html" || ct == "application/xhtml+xml"
}
