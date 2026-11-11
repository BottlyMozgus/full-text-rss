package server

// handlers_picker_test.go: tests for the interactive rule picker endpoints.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"rss-scraper/internal/model"
)

// pickerArticlePage is a fixture article served by the test's httptest
// site: one obvious content block, one ad to strip, one script.
const pickerArticlePage = `<!doctype html><html><head><title>Picker Fixture</title>
<meta http-equiv="refresh" content="9"><script>alert(1)</script></head><body>
<article id="main"><h1>Picked Article</h1><p>Paragraph one with plenty of text.</p>
<p>Paragraph two with more text to make readability happy.</p></article>
<div class="ad-banner">Buy things</div>
</body></html>`

// pickerFixture serves the article plus a feed-independent origin.
func pickerFixture(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/article.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pickerArticlePage))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// pickerArticleURL returns the fixture article URL.
func pickerArticleURL(t *testing.T, base string) string {
	t.Helper()
	return base + "/article.html"
}

func TestPickerPageURLFormAndItem(t *testing.T) {
	srv, st := testServer(t)
	h := srv.Routes()

	// No query: URL entry form.
	rec := authedGet(t, h, "/admin/rules/picker")
	if rec.Code != 200 {
		t.Fatalf("picker page = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="url"`) {
		t.Error("empty picker page lacks the URL entry form")
	}

	// Bad url: entry form plus flash, not a fetch attempt.
	rec = authedGet(t, h, "/admin/rules/picker?url=notaurl")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "http(s)") {
		t.Errorf("picker page for bad url = %d, want 200 with hint", rec.Code)
	}

	// item= with an existing item: dot carries URL and return path.
	feed := model.Feed{Token: "picktok", SourceURL: "https://src.example.test/feed.xml"}
	feedID, err := st.CreateFeed(context.Background(), &feed)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	item := &model.Item{GUID: "g1", URL: pickerArticleURL(t, "https://art.example.test"), Title: "An item"}
	if _, err := st.UpsertItem(context.Background(), feedID, item); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	rec = authedGet(t, h, fmt.Sprintf("/admin/rules/picker?item=%d", item.ID))
	if rec.Code != 200 {
		t.Fatalf("picker page for item = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/admin/rules/picker"`) {
		t.Error("item picker page lacks the save form")
	}
	if !strings.Contains(body, "art.example.test") {
		t.Error("picker page does not show the resolved host")
	}
	if !strings.Contains(body, `value="https://art.example.test/article.html"`) {
		t.Error("picker page lost the article URL")
	}

	// Open-redirect attempts fall back to /admin/rules.
	for _, bad := range []string{"//evil.example/x", "https://evil.example/x", "/a\\b"} {
		rec = authedGet(t, h, "/admin/rules/picker?url="+url.QueryEscape(pickerArticleURL(t, "https://art.example.test"))+"&return="+url.QueryEscape(bad))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `href="/admin/rules"`) {
			t.Errorf("return=%q not sanitized (code %d)", bad, rec.Code)
		}
	}
}

func TestPickerPageFeedShortcut(t *testing.T) {
	srv, st := testServer(t)
	h := srv.Routes()
	site := pickerFixture(t)

	feed := model.Feed{Token: "feedtok", SourceURL: site.URL + "/feed.xml"}
	feedID, err := st.CreateFeed(context.Background(), &feed)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}

	// Empty feed: friendly error, no redirect.
	rec := authedGet(t, h, fmt.Sprintf("/admin/rules/picker?feed=%d", feedID))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "no items yet") {
		t.Errorf("empty feed picker = %d, want 200 with hint", rec.Code)
	}

	item := &model.Item{GUID: "g9", URL: pickerArticleURL(t, "https://art.example.test")}
	if _, err := st.UpsertItem(context.Background(), feedID, item); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	rec = authedGet(t, h, fmt.Sprintf("/admin/rules/picker?feed=%d", feedID))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("feed shortcut = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, fmt.Sprintf("item=%d", item.ID)) {
		t.Errorf("redirect %q does not name the item", loc)
	}
}

func TestPickerFrameHeadersAndSanitizedBody(t *testing.T) {
	srv, _ := testServer(t)
	h := srv.Routes()
	site := pickerFixture(t)
	art := pickerArticleURL(t, site.URL)

	rec := authedGet(t, h, "/admin/rules/picker/frame?url="+url.QueryEscape(art))
	if rec.Code != 200 {
		t.Fatalf("frame = %d, want 200", rec.Code)
	}
	hdr := rec.Header()
	if csp := hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "script-src 'none'") {
		t.Errorf("frame CSP = %q", csp)
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Referrer-Policy") != "no-referrer" || hdr.Get("Cache-Control") != "no-store" {
		t.Error("frame missing hardening headers")
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Error("frame body contains script")
	}
	if !strings.Contains(body, "Picked Article") {
		t.Error("frame body lost the article content")
	}
	if !strings.Contains(body, `data-picker-error`) && !strings.Contains(body, "<base") {
		t.Error("frame body lacks base element")
	}

	// Bad url: 400 with the error marker document.
	rec = authedGet(t, h, "/admin/rules/picker/frame?url=bad")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "data-picker-error") {
		t.Errorf("frame bad url = %d, want 400 with marker", rec.Code)
	}

	// Fetch failure: 502 with marker.
	rec = authedGet(t, h, "/admin/rules/picker/frame?url="+url.QueryEscape(site.URL+"/missing"))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "data-picker-error") {
		t.Errorf("frame fetch failure = %d, want 502 with marker", rec.Code)
	}
}

func TestPickerPreviewJSON(t *testing.T) {
	srv, _ := testServer(t)
	h := srv.Routes()
	site := pickerFixture(t)
	art := pickerArticleURL(t, site.URL)

	post := func(t *testing.T, body string, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		rec := login(t, h, "pw")
		req := httptest.NewRequest("POST", "/admin/rules/picker/preview", strings.NewReader(body))
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req)
		return rec2
	}

	// Wrong content type: 415.
	if rec := post(t, `{}`, "text/plain"); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("preview without json content type = %d, want 415", rec.Code)
	}

	// Bad JSON: 400.
	if rec := post(t, `{bad`, "application/json"); rec.Code != http.StatusBadRequest {
		t.Errorf("preview bad json = %d, want 400", rec.Code)
	}

	// Happy path with content + strip selectors.
	payload := fmt.Sprintf(`{"url":%q,"content_selector":"article#main","strip_selectors":[".ad-banner","div[bad"]}`, art)
	rec := post(t, payload, "application/json")
	if rec.Code != 200 {
		t.Fatalf("preview = %d, want 200", rec.Code)
	}
	var resp struct {
		HTML           string   `json:"html"`
		Mode           string   `json:"mode"`
		TextLength     int      `json:"text_length"`
		ContentMatches int      `json:"content_matches"`
		StripMatches   []int    `json:"strip_matches"`
		ContentError   string   `json:"content_error"`
		StripErrors    []string `json:"strip_errors"`
		Error          string   `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("preview response not json: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("preview error: %s", resp.Error)
	}
	if !strings.Contains(resp.HTML, "Picked Article") || strings.Contains(resp.HTML, "Buy things") {
		t.Error("preview html not cleaned per selectors")
	}
	if resp.Mode != "rule" {
		t.Errorf("mode = %q, want rule", resp.Mode)
	}
	if resp.ContentMatches != 1 {
		t.Errorf("content_matches = %d, want 1", resp.ContentMatches)
	}
	if len(resp.StripMatches) != 2 || resp.StripMatches[0] != 1 || resp.StripMatches[1] != 0 {
		t.Errorf("strip_matches = %v, want [1 0]", resp.StripMatches)
	}
	if resp.StripErrors[0] != "" || resp.StripErrors[1] == "" {
		t.Errorf("strip_errors = %q, want [\"\" <msg>]", resp.StripErrors)
	}
	if resp.TextLength == 0 {
		t.Error("text_length not computed")
	}

	// Fetch failure (page missing): 502 JSON error.
	payload = fmt.Sprintf(`{"url":%q}`, site.URL+"/missing")
	rec = post(t, payload, "application/json")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("preview fetch failure = %d, want 502 json", rec.Code)
	}
	var errResp struct {
		Error string `json:"error"`
		HTML  string `json:"html"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if errResp.Error == "" || errResp.HTML != "" {
		t.Errorf("failed preview error=%q html=%q", errResp.Error, errResp.HTML)
	}
}

func TestPickerSaveCreatesUpdatesAndReextracts(t *testing.T) {
	srv, st := testServer(t)
	h := srv.Routes()
	site := pickerFixture(t)
	art := pickerArticleURL(t, site.URL)

	feed := model.Feed{Token: "savetok", SourceURL: site.URL + "/feed.xml"}
	feedID, _ := st.CreateFeed(context.Background(), &feed)
	item := &model.Item{GUID: "g2", URL: art, Title: "Save item", Summary: "old summary"}
	if _, err := st.UpsertItem(context.Background(), feedID, item); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	itemID := item.ID

	postForm := func(t *testing.T, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		rec := login(t, h, "pw")
		req := httptest.NewRequest("POST", "/admin/rules/picker", strings.NewReader(form.Encode()))
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req)
		return rec2
	}

	form := url.Values{
		"host":             {"art.example.test"},
		"content_selector": {"article#main"},
		"strip_selectors":  {".ad-banner"},
		"url":              {art},
		"return":           {fmt.Sprintf("/admin/feeds/%d/items/%d", feedID, itemID)},
		"item_id":          {fmt.Sprintf("%d", itemID)},
	}
	rec := postForm(t, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != fmt.Sprintf("/admin/feeds/%d/items/%d", feedID, itemID) {
		t.Errorf("redirect to %q", loc)
	}
	rule, err := st.RuleForHost(context.Background(), "art.example.test")
	if err != nil || rule == nil {
		t.Fatalf("rule not saved: %v", err)
	}
	if rule.ContentSelector != "article#main" || len(rule.StripSelectors) != 1 {
		t.Errorf("saved rule = %+v", rule)
	}

	// The item was re-extracted from the same cached page.
	saved, err := st.Item(context.Background(), itemID)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	if !saved.Extracted || !strings.Contains(saved.ContentHTML, "Picked Article") || strings.Contains(saved.ContentHTML, "Buy things") {
		t.Errorf("item not re-extracted: extracted=%v html=%.80q", saved.Extracted, saved.ContentHTML)
	}

	// Saving again updates in place (upsert by host).
	form.Set("content_selector", "article")
	if rec := postForm(t, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("second save = %d", rec.Code)
	}
	rules, _ := st.SiteRules(context.Background())
	if len(rules) != 1 || rules[0].ContentSelector != "article" {
		t.Errorf("rules after update = %+v", rules)
	}

	// Invalid selector: re-render with flash, nothing saved.
	form.Set("content_selector", "article[[")
	rec = postForm(t, form)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Invalid selector") {
		t.Errorf("invalid selector save = %d, want 200 with flash", rec.Code)
	}
	rule, _ = st.RuleForHost(context.Background(), "art.example.test")
	if rule.ContentSelector != "article" {
		t.Errorf("invalid save overwrote the rule: %+v", rule)
	}

	// Validation failure path: empty host re-renders with the submitted values.
	form2 := url.Values{"host": {""}, "content_selector": {"p"}, "url": {art}, "return": {"/admin/rules"}}
	if rec := postForm(t, form2); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Host is required") {
		t.Errorf("empty host save = %d, want 200 with flash", rec.Code)
	}
}

func TestPickerRequiresAuth(t *testing.T) {
	srv, _ := testServer(t)
	h := srv.Routes()
	for _, path := range []string{
		"/admin/rules/picker",
		"/admin/rules/picker/frame?url=https://x.example.test/",
	} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("GET %s unauthed = %d, want redirect", path, rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/admin/rules/picker/preview", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("POST preview unauthed = %d, want redirect", rec.Code)
	}
}
