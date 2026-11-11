package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"

	"rss-scraper/internal/model"
	"rss-scraper/internal/store"
)

// seedFeedWithNItems creates a feed with n items published one hour apart;
// "Item n" is the newest.
func seedFeedWithNItems(t *testing.T, st *store.Store, n int) *model.Feed {
	t.Helper()
	ctx := context.Background()
	f := &model.Feed{SourceURL: "https://src.example/feed.xml", Title: "Many", RefreshEvery: time.Hour}
	if _, err := st.CreateFeed(ctx, f); err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		it := &model.Item{
			GUID:        fmt.Sprintf("g-%d", i),
			URL:         fmt.Sprintf("https://src.example/%d", i),
			Title:       fmt.Sprintf("Item %d", i),
			ContentHTML: "<p>body</p>",
			Extracted:   true,
			PublishedAt: newNullTime(base.Add(time.Duration(i) * time.Hour)),
		}
		if _, err := st.UpsertItem(ctx, f.ID, it); err != nil {
			t.Fatalf("UpsertItem %d: %v", i, err)
		}
	}
	return f
}

// servedTitles fetches the token feed and returns its item titles in order.
func servedTitles(t *testing.T, h http.Handler, token string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/feed/"+token+".xml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("feed status = %d, want 200", rec.Code)
	}
	parsed, err := gofeed.NewParser().ParseString(rec.Body.String())
	if err != nil {
		t.Fatalf("parse feed: %v", err)
	}
	titles := make([]string, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		titles = append(titles, it.Title)
	}
	return titles
}

func TestFeedTokenHonoursMaxItems(t *testing.T) {
	s, st := testServer(t)
	f := seedFeedWithNItems(t, st, 25)
	h := s.Routes()

	// Default: the 20 newest of 25.
	titles := servedTitles(t, h, f.Token)
	if len(titles) != 20 {
		t.Fatalf("default served %d items, want 20", len(titles))
	}
	if titles[0] != "Item 25" || titles[19] != "Item 6" {
		t.Errorf("default range = %q .. %q, want Item 25 .. Item 6", titles[0], titles[19])
	}

	// Lowered through the API.
	rec, out := apiRequest(t, h, "PUT", apiPath("/api/feeds/", f.ID),
		`{"source_url":"https://src.example/feed.xml","title":"Many","max_items":3}`)
	if rec.Code != http.StatusOK || out["max_items"].(float64) != 3 {
		t.Fatalf("PUT max_items=3 -> %d %v", rec.Code, out)
	}
	titles = servedTitles(t, h, f.Token)
	if strings.Join(titles, ",") != "Item 25,Item 24,Item 23" {
		t.Errorf("max_items=3 served %v", titles)
	}

	// Raised above the cached count: everything cached is served.
	rec, _ = apiRequest(t, h, "PUT", apiPath("/api/feeds/", f.ID),
		`{"source_url":"https://src.example/feed.xml","title":"Many","max_items":100}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT max_items=100 -> %d", rec.Code)
	}
	if titles = servedTitles(t, h, f.Token); len(titles) != 25 {
		t.Errorf("max_items=100 served %d items, want 25", len(titles))
	}
}

func TestAPIFeedMaxItems(t *testing.T) {
	s, _ := testServer(t)
	h := s.Routes()

	// Omitted on create -> default 20.
	rec, out := apiRequest(t, h, "POST", "/api/feeds", `{"source_url":"https://x.example/f.xml"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", rec.Code)
	}
	if out["max_items"].(float64) != 20 {
		t.Errorf("default max_items = %v, want 20", out["max_items"])
	}
	path := apiPath("/api/feeds/", int64(out["id"].(float64)))

	// Explicit on create.
	rec, out = apiRequest(t, h, "POST", "/api/feeds", `{"source_url":"https://y.example/f.xml","max_items":500}`)
	if rec.Code != http.StatusCreated || out["max_items"].(float64) != 500 {
		t.Errorf("create max_items=500 -> %d %v", rec.Code, out["max_items"])
	}

	// Set, then an update that omits the field keeps the stored value.
	if rec, _ = apiRequest(t, h, "PUT", path, `{"source_url":"https://x.example/f.xml","max_items":7}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT max_items=7 = %d", rec.Code)
	}
	rec, out = apiRequest(t, h, "PUT", path, `{"source_url":"https://x.example/f.xml","title":"Renamed"}`)
	if rec.Code != http.StatusOK || out["max_items"].(float64) != 7 {
		t.Errorf("PUT without max_items -> %d %v, want 200 and 7", rec.Code, out["max_items"])
	}
	if _, out = apiRequest(t, h, "GET", path, ""); out["max_items"].(float64) != 7 {
		t.Errorf("GET max_items = %v, want 7", out["max_items"])
	}

	// Out of range is rejected on both verbs and leaves the value alone.
	for _, body := range []string{
		`{"source_url":"https://x.example/f.xml","max_items":-1}`,
		`{"source_url":"https://x.example/f.xml","max_items":501}`,
	} {
		if rec, _ = apiRequest(t, h, "POST", "/api/feeds", body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s -> %d, want 400", body, rec.Code)
		}
		if rec, _ = apiRequest(t, h, "PUT", path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s -> %d, want 400", body, rec.Code)
		}
	}
	if _, out = apiRequest(t, h, "GET", path, ""); out["max_items"].(float64) != 7 {
		t.Errorf("max_items after rejected updates = %v, want 7", out["max_items"])
	}
}

func TestFeedFormMaxItems(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()
	ctx := context.Background()

	// The new-feed form is prefilled with the default.
	rec := authedGet(t, h, "/admin/feeds/new")
	if !strings.Contains(rec.Body.String(), `name="max_items" value="20"`) {
		t.Error("new form does not prefill max_items=20")
	}

	// Invalid values re-render with an error and store nothing.
	for _, bad := range []string{"0", "501", "-3", "ten", "2.5"} {
		rec = postForm(t, h, "/admin/feeds/new", "source_url=https://x.example/f.xml&refresh_every=60m&max_items="+bad)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Max items must be") {
			t.Errorf("max_items=%s: status %d, error shown = %v", bad, rec.Code, strings.Contains(rec.Body.String(), "Max items must be"))
		}
	}
	if feeds, _ := st.Feeds(ctx); len(feeds) != 0 {
		t.Fatalf("invalid forms created %d feeds", len(feeds))
	}

	// Valid value is stored and shown on the edit form.
	rec = postForm(t, h, "/admin/feeds/new", "source_url=https://x.example/f.xml&refresh_every=60m&max_items=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200", rec.Code)
	}
	feeds, _ := st.Feeds(ctx)
	if len(feeds) != 1 || feeds[0].MaxItems != 7 {
		t.Fatalf("feeds after create = %+v, want one with MaxItems 7", feeds)
	}
	editPath := fmt.Sprintf("/admin/feeds/%d/edit", feeds[0].ID)
	if rec = authedGet(t, h, editPath); !strings.Contains(rec.Body.String(), `name="max_items" value="7"`) {
		t.Error("edit form does not show stored max_items=7")
	}

	// Edit changes it.
	rec = postForm(t, h, editPath, "source_url=https://x.example/f.xml&refresh_every=60m&max_items=42")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update = %d, want 303", rec.Code)
	}
	if got, _ := st.Feed(ctx, feeds[0].ID); got.MaxItems != 42 {
		t.Errorf("MaxItems after edit = %d, want 42", got.MaxItems)
	}

	// A create without the field falls back to the default.
	postForm(t, h, "/admin/feeds/new", "source_url=https://z.example/f.xml&refresh_every=60m")
	feeds, _ = st.Feeds(ctx)
	if len(feeds) != 2 || feeds[1].MaxItems != model.DefaultMaxItems {
		t.Errorf("feeds after fieldless create = %+v, want second with MaxItems %d", feeds, model.DefaultMaxItems)
	}
}
