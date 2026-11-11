package server

// handlers_reextract_test.go: the per-item re-extract button.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"rss-scraper/internal/model"
)

func TestItemReextractAppliesRulesAndHandlesFailures(t *testing.T) {
	srv, st := testServer(t)
	h := srv.Routes()
	site := pickerFixture(t)
	art := pickerArticleURL(t, site.URL)

	feed := model.Feed{Token: "retok", SourceURL: site.URL + "/feed.xml"}
	feedID, _ := st.CreateFeed(context.Background(), &feed)
	item := &model.Item{GUID: "g-re", URL: art, Title: "Re item", Summary: "old summary"}
	if _, err := st.UpsertItem(context.Background(), feedID, item); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}

	// Unknown item -> 404. (The route matches by item id; a mismatched
	// feed id in the path is not addressed, so only the item is checked.)
	target := fmt.Sprintf("/admin/feeds/%d/items/%d/reextract", feedID, item.ID+999)
	if rec := postForm(t, h, target, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown item reextract = %d, want 404", rec.Code)
	}

	// No rule yet: readability path, page cached, item re-extracted.
	target = fmt.Sprintf("/admin/feeds/%d/items/%d/reextract", feedID, item.ID)
	rec := postForm(t, h, target, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reextract without rule = %d body=%.120s, want 303", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != fmt.Sprintf("/admin/feeds/%d/items/%d", feedID, item.ID) {
		t.Errorf("redirect to %q", loc)
	}
	saved, err := st.Item(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	if !saved.Extracted || !strings.Contains(saved.ContentHTML, "Picked Article") {
		t.Errorf("reextract without rule = extracted=%v html=%.80q", saved.Extracted, saved.ContentHTML)
	}

	// Strip rule saved through the picker (caches the page): re-extract
	// drops the ad.
	form := url.Values{
		"url":              {art},
		"host":             {"art.example.test"},
		"strip_selectors":  {".ad-banner"},
		"content_selector": {"article#main"},
		"return":           {fmt.Sprintf("/admin/feeds/%d/items/%d", feedID, item.ID)},
		"item_id":          {fmt.Sprintf("%d", item.ID)},
	}
	rec = postForm(t, h, "/admin/rules/picker", form.Encode())
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("picker save = %d, want 303", rec.Code)
	}
	rule, err := st.RuleForHost(context.Background(), "art.example.test")
	if err != nil || rule == nil {
		t.Fatalf("RuleForHost: %v %v", rule, err)
	}
	if rec := postForm(t, h, target, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("reextract with rule = %d", rec.Code)
	}
	saved, _ = st.Item(context.Background(), item.ID)
	if strings.Contains(saved.ContentHTML, "Buy things") {
		t.Error("reextract did not apply the strip selector")
	}
	if !strings.Contains(saved.ContentHTML, "Picked Article") {
		t.Errorf("content lost on rule reextract: %.80q", saved.ContentHTML)
	}

	// The re-extracted time is visible (FetchedAt set by UpdateItemContent).
	if !saved.FetchedAt.Valid {
		t.Error("reextract did not update fetched_at")
	}
}
