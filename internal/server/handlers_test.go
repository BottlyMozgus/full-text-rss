package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rss-scraper/internal/model"
)

func TestAPIRulesLifecycle(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()

	// Create.
	rec, out := apiRequest(t, h, "POST", "/api/rules",
		`{"host":"Example.COM.","content_selector":"article.post","strip_selectors":[".share",".ads"],"url_rewrite_from":"/story/","url_rewrite_to":"/amp/story/"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body.String())
	}
	id := int64(out["id"].(float64))
	if out["host"] != "example.com" {
		t.Errorf("host = %v, want lowercased trimmed", out["host"])
	}
	sels, _ := out["strip_selectors"].([]any)
	if len(sels) != 2 || sels[0] != ".share" || sels[1] != ".ads" {
		t.Errorf("strip_selectors = %v", sels)
	}

	// List.
	rec, _ = apiRequest(t, h, "GET", "/api/rules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}

	// Get.
	rec, out = apiRequest(t, h, "GET", apiPath("/api/rules/", id), "")
	if rec.Code != http.StatusOK || out["host"] != "example.com" {
		t.Fatalf("get = %d %v", rec.Code, out)
	}

	// Update in place (same host).
	rec, out = apiRequest(t, h, "PUT", apiPath("/api/rules/", id),
		`{"host":"example.com","content_selector":"div.main","strip_selectors":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.SiteRule(context.Background(), id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.ContentSelector != "div.main" || len(got.StripSelectors) != 0 {
		t.Errorf("stored = %+v", got)
	}

	// Update with host change: new row, old row gone.
	rec, out = apiRequest(t, h, "PUT", apiPath("/api/rules/", id),
		`{"host":"other.example","content_selector":"#content"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("host-change update = %d body=%s", rec.Code, rec.Body.String())
	}
	if out["host"] != "other.example" {
		t.Fatalf("host after change = %v", out["host"])
	}
	if _, err := st.SiteRule(context.Background(), id); err == nil {
		t.Error("old rule row still exists after host change")
	}

	// Validation.
	for _, body := range []string{
		`{"content_selector":"x"}`,                   // no host
		`{"host":"h.example","url_rewrite_to":"/x"}`, // rewrite_to without from
		`not json`,
	} {
		rec, _ = apiRequest(t, h, "POST", "/api/rules", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s -> %d, want 400", body, rec.Code)
		}
	}

	// Delete.
	rec, out = apiRequest(t, h, "DELETE", apiPath("/api/rules/", int64(out["id"].(float64))), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	rules, _ := st.SiteRules(context.Background())
	if len(rules) != 0 {
		t.Errorf("rules after delete = %d, want 0", len(rules))
	}
	rec, _ = apiRequest(t, h, "GET", apiPath("/api/rules/", 999), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown rule = %d, want 404", rec.Code)
	}
}

// postForm submits a form-encoded POST as an authed browser.
func postForm(t *testing.T, h http.Handler, target, form string) *httptest.ResponseRecorder {
	t.Helper()
	rec := login(t, h, "pw")
	req := httptest.NewRequest("POST", target, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	return rec2
}

func TestRulesFormPostsNewlineStripSelectors(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()
	form := "host=site.example" +
		"&content_selector=article.post" +
		"&strip_selectors=" + "..share%0A..ads%0A%0A..comments" +
		"&rewrite_from=/story/&rewrite_to=/amp/story/"
	// URL-encode the newlines properly: %0A between selectors.
	form = strings.Replace(form, "..share", ".share", 1)
	form = strings.Replace(form, "..ads", ".ads", 1)
	form = strings.Replace(form, "..comments", ".comments", 1)
	rec := postForm(t, h, "/admin/rules/new", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create rule via form = %d, want 303; body=%s", rec.Code, rec.Body.String()[:min(len(rec.Body.String()), 400)])
	}
	rules, err := st.SiteRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(rules))
	}
	r := rules[0]
	if r.Host != "site.example" || r.ContentSelector != "article.post" {
		t.Errorf("rule = %+v", r)
	}
	want := []string{".share", ".ads", ".comments"}
	if len(r.StripSelectors) != len(want) {
		t.Fatalf("strip_selectors = %v, want %v", r.StripSelectors, want)
	}
	for i := range want {
		if r.StripSelectors[i] != want[i] {
			t.Errorf("strip_selectors[%d] = %q, want %q", i, r.StripSelectors[i], want[i])
		}
	}
}

func TestRulesFormValidationRerenders(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()
	// Missing host.
	rec := postForm(t, h, "/admin/rules/new", "host=&content_selector=article")
	if rec.Code != http.StatusOK {
		t.Fatalf("missing host = %d, want 200 re-render", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Host is required.") {
		t.Error("no validation flash in re-render")
	}
	rules, _ := st.SiteRules(context.Background())
	if len(rules) != 0 {
		t.Errorf("invalid form created %d rules", len(rules))
	}
}

func TestFeedFormNewAndValidation(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()

	// Empty form renders.
	rec := authedGet(t, h, "/admin/feeds/new")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Add feed") {
		t.Fatalf("form = %d; missing heading", rec.Code)
	}

	// Bad interval -> re-render with error, nothing stored.
	rec = postForm(t, h, "/admin/feeds/new", "source_url=https://x.example/f.xml&title=&refresh_every=notaduration")
	if rec.Code != http.StatusOK {
		t.Fatalf("bad interval = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not a duration") {
		t.Error("no interval error flash")
	}
	feeds, _ := st.Feeds(context.Background())
	if len(feeds) != 0 {
		t.Fatalf("invalid form created %d feeds", len(feeds))
	}

	// Valid form -> edit-shaped page with the feed URL.
	rec = postForm(t, h, "/admin/feeds/new", "source_url=https://x.example/f.xml&title=My+Feed&refresh_every=60m")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid create = %d, want 200; body head: %s", rec.Code, rec.Body.String()[:min(len(rec.Body.String()), 300)])
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/feed/") || !strings.Contains(body, ".xml") {
		t.Errorf("created page missing feed URL; head: %.300s", body)
	}
	feeds, _ = st.Feeds(context.Background())
	if len(feeds) != 1 {
		t.Fatalf("feeds = %d, want 1", len(feeds))
	}
	if feeds[0].RefreshEvery != 60*60*1e9 {
		t.Errorf("RefreshEvery = %v, want 1h", feeds[0].RefreshEvery)
	}
}

func TestFeedEditFormShowsTokenURL(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()
	feed, _, _ := seedFeedWithItems(t, st)
	rec := authedGet(t, h, apiPath("/admin/feeds/", feed.ID)+"/edit")
	if rec.Code != http.StatusOK {
		t.Fatalf("edit form = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/feed/"+feed.Token+".xml") {
		t.Errorf("edit form missing token URL:\n%.400s", body)
	}
	if !strings.Contains(body, `data-copy="#feed-url"`) {
		t.Error("edit form missing copy button wiring")
	}
}

func TestRuleEditFormRenders(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()
	ctx := context.Background()
	id, err := st.UpsertSiteRule(ctx, &model.SiteRule{Host: "edit.example", ContentSelector: "main"})
	if err != nil {
		t.Fatal(err)
	}
	rec := authedGet(t, h, apiPath("/admin/rules/", id)+"/edit")
	if rec.Code != http.StatusOK {
		t.Fatalf("rule edit = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `value="edit.example"`) {
		t.Error("rule edit form missing host value")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
