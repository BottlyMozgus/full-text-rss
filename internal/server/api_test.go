package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// apiRequest performs an API request with bearer auth and decodes JSON
// objects; array responses keep the raw recorder only.
func apiRequest(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Code != http.StatusNotFound && strings.Contains(rec.Header().Get("Content-Type"), "json") && rec.Body.Len() > 0 {
		var probe any
		if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
			t.Fatalf("%s %s: decode response %q: %v", method, target, rec.Body.String(), err)
		}
		if obj, ok := probe.(map[string]any); ok {
			out = obj
		}
	}
	return rec, out
}

func TestAPICreateFeedGeneratesToken(t *testing.T) {
	s, _ := testServer(t)
	h := s.Routes()
	rec, out := apiRequest(t, h, "POST", "/api/feeds",
		`{"source_url":"https://src.example/feed.xml","title":"API feed","refresh_every_seconds":1800}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	token, _ := out["token"].(string)
	if len(token) != 32 {
		t.Errorf("token = %q, want 32 hex chars", token)
	}
	if out["refresh_every_seconds"].(float64) != 1800 {
		t.Errorf("refresh_every_seconds = %v, want 1800", out["refresh_every_seconds"])
	}
	if out["title"] != "API feed" {
		t.Errorf("title = %v", out["title"])
	}
	if fu, _ := out["feed_url"].(string); !strings.Contains(fu, "/feed/"+token+".xml") {
		t.Errorf("feed_url = %v, want absolute /feed/<token>.xml", out["feed_url"])
	}
}

func TestAPICreateFeedValidation(t *testing.T) {
	s, _ := testServer(t)
	h := s.Routes()
	cases := []struct {
		body string
		want int
	}{
		{`{"title":"no url","refresh_every_seconds":60}`, http.StatusBadRequest},
		{`{"source_url":"https://x.example/f.xml","refresh_every_seconds":-5}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		rec, _ := apiRequest(t, h, "POST", "/api/feeds", c.body)
		if rec.Code != c.want {
			t.Errorf("body %s -> %d, want %d", c.body, rec.Code, c.want)
		}
	}
}

func TestAPIFeedLifecycle(t *testing.T) {
	s, st := testServer(t)
	h := s.Routes()

	// Create.
	rec, out := apiRequest(t, h, "POST", "/api/feeds",
		`{"source_url":"https://src.example/f.xml","title":"Lifecycle","refresh_every_seconds":3600}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d", rec.Code)
	}
	id := int64(out["id"].(float64))

	// List contains it.
	rec, _ = apiRequest(t, h, "GET", "/api/feeds", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	// list decodes into an array.
	var feeds []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &feeds); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(feeds) != 1 || feeds[0]["id"].(float64) != float64(id) {
		t.Fatalf("list = %v", feeds)
	}

	// Get.
	rec, out = apiRequest(t, h, "GET", apiPath("/api/feeds/", id), "")
	if rec.Code != http.StatusOK || out["id"].(float64) != float64(id) {
		t.Fatalf("get = %d %v", rec.Code, out)
	}

	// Update.
	rec, out = apiRequest(t, h, "PUT", apiPath("/api/feeds/", id),
		`{"source_url":"https://src.example/other.xml","title":"Renamed","refresh_every_seconds":7200}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d body=%s", rec.Code, rec.Body.String())
	}
	if out["title"] != "Renamed" || out["refresh_every_seconds"].(float64) != 7200 {
		t.Fatalf("update result = %v", out)
	}
	f, err := st.Feed(t.Context(), id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if f.SourceURL != "https://src.example/other.xml" || f.RefreshEvery != 2*time.Hour {
		t.Errorf("stored = %+v", f)
	}

	// Delete cascades.
	rec, _ = apiRequest(t, h, "DELETE", apiPath("/api/feeds/", id), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	if _, err := st.Feed(t.Context(), id); err == nil {
		t.Fatal("feed still exists after delete")
	}

	// Get after delete -> 404.
	rec, _ = apiRequest(t, h, "GET", apiPath("/api/feeds/", id), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted = %d, want 404", rec.Code)
	}
	// Unknown id -> 404.
	rec, _ = apiRequest(t, h, "GET", "/api/feeds/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", rec.Code)
	}
	// Bad id -> 404 (not 500).
	rec, _ = apiRequest(t, h, "GET", "/api/feeds/abc", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad id = %d, want 404", rec.Code)
	}
}

// apiPath builds "/api/feeds/<id>".
func apiPath(prefix string, id int64) string {
	return prefix + int64ToString(id)
}

func int64ToString(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestAPIRefreshFeedUnknownID(t *testing.T) {
	s, _ := testServer(t)
	rec, _ := apiRequest(t, s.Routes(), "POST", "/api/feeds/999/refresh", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("refresh unknown = %d, want 404", rec.Code)
	}
}

func TestAPIFeedStatus(t *testing.T) {
	s, st := testServer(t)
	feed, _, _ := seedFeedWithItems(t, st)
	rec, out := apiRequest(t, s.Routes(), "GET", apiPath("/api/feeds/", feed.ID)+"/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	items, _ := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	first := items[0].(map[string]any)
	if first["state"] != "extracted" {
		t.Errorf("item1 state = %v, want extracted", first["state"])
	}
	second := items[1].(map[string]any)
	if second["state"] != "summary-fallback" {
		t.Errorf("item2 state = %v, want summary-fallback", second["state"])
	}
	if out["last_status"] != "" {
		t.Errorf("last_status = %v, want empty (never refreshed)", out["last_status"])
	}
}
