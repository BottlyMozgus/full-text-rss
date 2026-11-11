package server

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"
)

// parsedEncoded pulls content:encoded out of a re-parsed item (gofeed v1.3
// maps it to Item.Content; older versions to Extensions).
func parsedEncoded(t *testing.T, it *gofeed.Item) string {
	t.Helper()
	if it.Content != "" {
		return it.Content
	}
	if enc, ok := it.Extensions["content"]["encoded"]; ok && len(enc) > 0 {
		return enc[0].Value
	}
	t.Fatal("content:encoded missing on parsed item")
	return ""
}

func TestFeedTokenServesRSS(t *testing.T) {
	s, st := testServer(t)
	feed, _, _ := seedFeedWithItems(t, st)
	h := s.Routes()

	req := httptest.NewRequest("GET", "/feed/"+feed.Token+".xml", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Errorf("content type = %q, want application/rss+xml", ct)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag header")
	}
	if rec.Header().Get("Last-Modified") == "" {
		t.Error("no Last-Modified header (feed not refreshed yet, CreatedAt expected)")
	}

	parsed, err := gofeed.NewParser().ParseString(rec.Body.String())
	if err != nil {
		t.Fatalf("output does not parse as RSS: %v\n%s", err, rec.Body.String())
	}
	if parsed.Title != "Seed Feed" {
		t.Errorf("title = %q, want Seed Feed", parsed.Title)
	}
	if parsed.Link != "https://src.example/feed.xml" {
		t.Errorf("link = %q", parsed.Link)
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(parsed.Items))
	}

	// Item 1: extracted content lands in content:encoded.
	it := parsed.Items[0]
	if it.Title != "Extracted article" {
		t.Fatalf("item1 = %q", it.Title)
	}
	// No <img> in the article, so the built-in placeholder leads the body.
	wantEnc := `<p><img src="http://example.com/static/img/fallback.png" alt=""></p><p>full text body</p>`
	if enc := parsedEncoded(t, it); enc != wantEnc {
		t.Errorf("item1 content:encoded = %q, want %q", enc, wantEnc)
	}
	if it.Image == nil && len(it.Enclosures) == 0 {
		t.Errorf("item1 has no enclosure/image for the placeholder")
	}
	if it.Description != "short summary" {
		t.Errorf("item1 description = %q, want original summary", it.Description)
	}

	// Item 2: failed extraction keeps the summary with fallback marker.
	it = parsed.Items[1]
	enc := parsedEncoded(t, it)
	if !strings.HasPrefix(enc, "<!--summary-fallback-->") {
		t.Errorf("item2 content:encoded = %q, want fallback marker prefix", enc)
	}
	if !strings.Contains(enc, "fallback summary text") {
		t.Errorf("item2 content:encoded missing summary text: %q", enc)
	}

	// XML sanity: exactly one root <rss> element (no CDATA breakage).
	var root struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Errorf("raw xml unmarshal: %v", err)
	}
	if root.XMLName.Local != "rss" {
		t.Errorf("root = %q, want rss", root.XMLName.Local)
	}
}

func TestFeedTokenWithoutXmlSuffix(t *testing.T) {
	s, st := testServer(t)
	feed, _, _ := seedFeedWithItems(t, st)
	req := httptest.NewRequest("GET", "/feed/"+feed.Token, nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestFeedTokenWrongToken404(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest("GET", "/feed/deadbeefdeadbeefdeadbeefdeadbeef.xml", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "token") || strings.Contains(body, "feed") {
		t.Errorf("404 body leaks details: %q", body)
	}
}

func TestFeedTokenNoAuthRequired(t *testing.T) {
	// The token IS the auth: no cookie, no bearer, still 200.
	s, st := testServer(t)
	feed, _, _ := seedFeedWithItems(t, st)
	req := httptest.NewRequest("GET", "/feed/"+feed.Token+".xml", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (token auth)", rec.Code)
	}
}

func TestFeedTokenETag304(t *testing.T) {
	s, st := testServer(t)
	feed, _, _ := seedFeedWithItems(t, st)
	h := s.Routes()

	first := httptest.NewRequest("GET", "/feed/"+feed.Token+".xml", nil)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, first)
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on first response")
	}

	second := httptest.NewRequest("GET", "/feed/"+feed.Token+".xml", nil)
	second.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, second)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match = %d, want 304", rec2.Code)
	}

	// A different etag must not 304.
	third := httptest.NewRequest("GET", "/feed/"+feed.Token+".xml", nil)
	third.Header.Set("If-None-Match", `"other"`)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, third)
	if rec3.Code != http.StatusOK {
		t.Fatalf("changed etag = %d, want 200", rec3.Code)
	}
}

func TestFeedTokenETagWeakPrefixAccepted(t *testing.T) {
	s, st := testServer(t)
	feed, _, _ := seedFeedWithItems(t, st)
	h := s.Routes()
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest("GET", "/feed/"+feed.Token+".xml", nil))
	etag := rec1.Header().Get("ETag")

	second := httptest.NewRequest("GET", "/feed/"+feed.Token+".xml", nil)
	second.Header.Set("If-None-Match", "W/"+etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, second)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("weak If-None-Match = %d, want 304", rec2.Code)
	}
}
