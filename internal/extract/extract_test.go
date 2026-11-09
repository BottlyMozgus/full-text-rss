package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rss-scraper/internal/config"
	"rss-scraper/internal/model"
)

func testConfig(spacing, jitter time.Duration) *config.Config {
	return &config.Config{
		FetchTimeout: 5 * time.Second,
		FetchSpacing: spacing,
		FetchJitter:  jitter,
		UserAgent:    "rss-scraper-test/1.0",
	}
}

// --- Fetcher ---

func TestFetchSuccessAndUserAgent(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		fmt.Fprint(w, "<html><body>hi</body></html>")
	}))
	defer srv.Close()

	f := NewFetcher(testConfig(0, 0))
	body, err := f.Fetch(context.Background(), srv.URL+"/page")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(string(body), "hi") {
		t.Fatalf("body = %q, want page content", body)
	}
	if gotUA != "rss-scraper-test/1.0" {
		t.Errorf("User-Agent = %q, want configured value", gotUA)
	}
	if gotAccept == "" {
		t.Error("Accept header missing")
	}
}

func TestFetchFollowsRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			fmt.Fprint(w, "<html><body>landing</body></html>")
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer srv.Close()

	f := NewFetcher(testConfig(0, 0))
	body, err := f.Fetch(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(string(body), "landing") {
		t.Fatalf("body = %q, want redirect followed", body)
	}
}

func TestFetch429TypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f := NewFetcher(testConfig(0, 0))
	_, err := f.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("Fetch on 429: want error")
	}
	if !strings.Contains(err.Error(), "429") && err != ErrRateLimited {
		var target = ErrRateLimited
		if !strings.Contains(err.Error(), target.Error()) {
			t.Fatalf("err = %v, want ErrRateLimited wrapped", err)
		}
	}
}

func TestFetch403TypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	f := NewFetcher(testConfig(0, 0))
	_, err := f.Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), ErrForbidden.Error()) {
		t.Fatalf("err = %v, want ErrForbidden wrapped", err)
	}
}

func TestFetchOtherNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := NewFetcher(testConfig(0, 0))
	_, err := f.Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), ErrExtract.Error()) {
		t.Fatalf("err = %v, want ErrExtract wrapped", err)
	}
}

func TestFetchResponseSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Write slightly more than the 5MB cap.
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i <= maxResponseSize/(1<<20); i++ {
			fmt.Fprint(w, chunk)
		}
	}))
	defer srv.Close()

	f := NewFetcher(testConfig(0, 0))
	_, err := f.Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), ErrExtract.Error()) {
		t.Fatalf("err = %v, want ErrExtract for oversized body", err)
	}
}

func TestSameHostSpacedDifferentHostsParallel(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	newSrv := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			counts[r.Host]++
			mu.Unlock()
			fmt.Fprint(w, "<html><body>x</body></html>")
		}))
	}
	// Three distinct httptest servers = three distinct hosts (127.0.0.1 with
	// different ports).
	s1, s2, s3 := newSrv(), newSrv(), newSrv()
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	f := NewFetcher(testConfig(0, 0)) // no spacing: parallelism visible

	// Different hosts must proceed in parallel: 3 fetches should complete far
	// below a serial-spacing budget.
	start := time.Now()
	var wg sync.WaitGroup
	for _, s := range []*httptest.Server{s1, s2, s3} {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			if _, err := f.Fetch(context.Background(), u); err != nil {
				t.Errorf("Fetch %s: %v", u, err)
			}
		}(s.URL)
	}
	wg.Wait()
	elapsed := time.Since(start)
	// Serial execution with even 50ms spacing per host would take >100ms.
	// Allow generous slop for slow CI.
	if elapsed > 100*time.Millisecond {
		t.Errorf("3 parallel host fetches took %v, want <100ms", elapsed)
	}

	// Same host must be spaced: with spacing 100ms, two sequential fetches to
	// the same host take >= 100ms.
	spaced := NewFetcher(testConfig(100*time.Millisecond, 0))
	start = time.Now()
	for i := 0; i < 3; i++ {
		if _, err := spaced.Fetch(context.Background(), s1.URL+"/loop"); err != nil {
			t.Fatalf("same-host fetch %d: %v", i, err)
		}
	}
	elapsed = time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Errorf("3 same-host fetches with 100ms spacing took %v, want >=200ms", elapsed)
	}
}

// --- rule source stub ---

type mapRules map[string]*model.SiteRule

func (m mapRules) RuleForHost(_ context.Context, host string) (*model.SiteRule, error) {
	if r, ok := m[host]; ok {
		return r, nil
	}
	return nil, nil
}

func ruleFor(host, contentSel string, strip []string, from, to string) *model.SiteRule {
	return &model.SiteRule{
		Host:            host,
		ContentSelector: contentSel,
		StripSelectors:  strip,
		URLRewriteFrom:  from,
		URLRewriteTo:    to,
	}
}

// --- Extractor ---

const articleHTML = `<!doctype html>
<html><head><title>Full Article | example.test</title>
<script>window.tracker=1;</script>
<style>body{color:red}</style></head>
<body>
<nav>menu <a href="/nav">nav link</a></nav>
<div class="article-body">
  <h1 class="headline">Real Headline</h1>
  <p class="lede">First paragraph with a <a class="lnk" href="/related">relative link</a> and an image
     <img class="pic" src="/img/pic.jpg" alt="a picture" onerror="evil()">.</p>
  <div class="share-bar">share buttons</div>
  <script>bad();</script>
  <blockquote>quoted</blockquote>
</div>
<footer>footer junk</footer>
</body></html>`

func servePage(t *testing.T, html string, ct string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		fmt.Fprint(w, html)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExtractReadability(t *testing.T) {
	srv := servePage(t, articleHTML, "")
	e := NewExtractor(NewFetcher(testConfig(0, 0)), mapRules{})
	res, err := e.Extract(context.Background(), srv.URL+"/story/1")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !res.Extracted {
		t.Error("Extracted = false, want true")
	}
	if !strings.Contains(res.Title, "Full Article") {
		t.Errorf("Title = %q, want page title", res.Title)
	}
	if !strings.Contains(res.ContentHTML, "First paragraph") {
		t.Errorf("ContentHTML missing readability content: %q", res.ContentHTML)
	}
	assertClean(t, res.ContentHTML)
	assertAbsolute(t, res.ContentHTML, srv.URL)
}

func TestRuleOverrideBeatsReadability(t *testing.T) {
	srv := servePage(t, articleHTML, "")
	rules := mapRules{
		hostOf(srv.URL): ruleFor(hostOf(srv.URL), "div.article-body", []string{".share-bar"}, "", ""),
	}
	e := NewExtractor(NewFetcher(testConfig(0, 0)), rules)
	res, err := e.Extract(context.Background(), srv.URL+"/story/2")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Rule content must be the article body: contains headline and lede.
	if !strings.Contains(res.ContentHTML, "Real Headline") {
		t.Errorf("rule extraction lost headline: %q", res.ContentHTML)
	}
	if !strings.Contains(res.ContentHTML, "First paragraph") {
		t.Errorf("rule extraction lost lede: %q", res.ContentHTML)
	}
	// Rule selection must exclude nav and footer that readability might keep.
	if strings.Contains(res.ContentHTML, "menu") || strings.Contains(res.ContentHTML, "nav link") {
		t.Errorf("rule extraction leaked nav: %q", res.ContentHTML)
	}
	if strings.Contains(res.ContentHTML, "footer junk") {
		t.Errorf("rule extraction leaked footer: %q", res.ContentHTML)
	}
	// Strip selector removed share bar.
	if strings.Contains(res.ContentHTML, "share buttons") {
		t.Errorf("strip selector did not remove share bar: %q", res.ContentHTML)
	}
	assertClean(t, res.ContentHTML)
	assertAbsolute(t, res.ContentHTML, srv.URL)
}

func TestRuleURLRewrite(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/amp/story/3" {
			fmt.Fprint(w, articleHTML)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	rules := mapRules{
		hostOf(srv.URL): ruleFor(hostOf(srv.URL), "div.article-body", nil, "/story/", "/amp/story/"),
	}
	e := NewExtractor(NewFetcher(testConfig(0, 0)), rules)
	res, err := e.Extract(context.Background(), srv.URL+"/story/3")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if hits != 1 {
		t.Fatalf("server hits = %d, want exactly the rewritten URL", hits)
	}
	if !strings.Contains(res.ContentHTML, "First paragraph") {
		t.Errorf("ContentHTML = %q, want rewritten-URL content", res.ContentHTML)
	}
}

func TestRuleEmptyContentFallsBackToReadability(t *testing.T) {
	srv := servePage(t, articleHTML, "")
	// Selector that matches nothing.
	rules := mapRules{
		hostOf(srv.URL): ruleFor(hostOf(srv.URL), "div.does-not-exist", nil, "", ""),
	}
	e := NewExtractor(NewFetcher(testConfig(0, 0)), rules)
	res, err := e.Extract(context.Background(), srv.URL+"/story/4")
	if err != nil {
		t.Fatalf("Extract with dead selector: %v (want readability fallback)", err)
	}
	if !strings.Contains(res.ContentHTML, "First paragraph") {
		t.Errorf("fallback content = %q, want readability output", res.ContentHTML)
	}
}

func TestPDFContentTypeErrExtract(t *testing.T) {
	srv := servePage(t, "%PDF-1.4 fake", "application/pdf")
	e := NewExtractor(NewFetcher(testConfig(0, 0)), mapRules{})
	_, err := e.Extract(context.Background(), srv.URL+"/paper.pdf")
	if err == nil || !strings.Contains(err.Error(), ErrExtract.Error()) {
		t.Fatalf("err = %v, want ErrExtract for PDF content type", err)
	}
}

func TestImageContentTypeErrExtract(t *testing.T) {
	srv := servePage(t, "\xff\xd8\xff jpeg", "image/jpeg")
	e := NewExtractor(NewFetcher(testConfig(0, 0)), mapRules{})
	_, err := e.Extract(context.Background(), srv.URL+"/pic.jpg")
	if err == nil || !strings.Contains(err.Error(), ErrExtract.Error()) {
		t.Fatalf("err = %v, want ErrExtract for image content type", err)
	}
}

func TestExtractGarbageErrExtract(t *testing.T) {
	srv := servePage(t, "\x00\x01\x02 not html at all", "application/octet-stream")
	e := NewExtractor(NewFetcher(testConfig(0, 0)), mapRules{})
	_, err := e.Extract(context.Background(), srv.URL+"/blob")
	if err == nil || !strings.Contains(err.Error(), ErrExtract.Error()) {
		t.Fatalf("err = %v, want ErrExtract for non-HTML", err)
	}
}

func TestCleanupStripsDangerousMarkup(t *testing.T) {
	nasty := `<div class="wrap" id="w" style="color:red" onclick="evil()">
		<script>alert(1)</script>
		<iframe src="https://evil.test"></iframe>
		<p class="keep-me" style="font-weight:bold">text with <strong class="s">bold</strong>
		<a href="/rel" class="link" target="_blank">link</a>
		<img src="/i.png" class="img" alt="pic" width="10" style="border:0">
		<em>em</em> <code>code</code></p>
		<ul><li class="li">one</li><li>two</li></ul>
		<table class="t"><thead><tr><th>h</th></tr></thead><tbody><tr><td>d</td></tr></tbody></table>
	</div>`
	cleaned := Clean(nasty)
	assertClean(t, cleaned)
	for _, want := range []string{"text with", "<strong>bold</strong>", `href="`, `src="`, "alt=\"pic\"", "<em>em</em>", "<code>code</code>", "<li>one</li>", "<table>", "<th>h</th>", "<td>d</td>"} {
		if !strings.Contains(cleaned, want) {
			t.Errorf("cleaned missing %q: %s", want, cleaned)
		}
	}
}

// assertClean fails when html contains anything the strict policy forbids:
// scripts, styles, iframes, classes, ids, inline style attributes, event
// handlers.
func assertClean(t *testing.T, html string) {
	t.Helper()
	for _, forbidden := range []string{"<script", "<style", "<iframe", "class=", "id=", "style=", "onclick", "onerror", "javascript:"} {
		if strings.Contains(strings.ToLower(html), forbidden) {
			t.Errorf("cleaned html contains %q: %s", forbidden, html)
		}
	}
}

// assertAbsolute fails when html contains a relative href/src URL.
func assertAbsolute(t *testing.T, html, base string) {
	t.Helper()
	if strings.Contains(html, `href="/`) || strings.Contains(html, `src="/`) {
		t.Errorf("html still has relative urls: %s", html)
	}
	if !strings.Contains(html, base) {
		t.Errorf("html has no absolute %s urls: %s", base, html)
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://www.example.com/a?b=1": "www.example.com",
		"http://EXAMPLE.com/x":          "example.com",
		"not a url":                     "",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContentTypeIsHTML(t *testing.T) {
	cases := map[string]bool{
		"text/html":                true,
		"text/html; charset=utf-8": true,
		"application/xhtml+xml":    true,
		"":                         true,
		"application/pdf":          false,
		"image/jpeg":               false,
		"application/json":         false,
		"text/plain":               false,
	}
	for in, want := range cases {
		if got := contentTypeIsHTML(in); got != want {
			t.Errorf("contentTypeIsHTML(%q) = %v, want %v", in, got, want)
		}
	}
}
