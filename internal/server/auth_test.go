package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"full-text-rss/internal/config"
	"full-text-rss/internal/model"
	"full-text-rss/internal/store"
)

// testServer builds a Server over a fresh store with password "pw" and
// token "tok".
func testServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	cfg := &config.Config{
		AdminPassword: "pw",
		AdminToken:    "tok",
		FetchSpacing:  time.Millisecond,
		FetchJitter:   0,
		FetchTimeout:  2 * time.Second,
		UserAgent:     "test",
	}
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(cfg, st), st
}

// login posts the password and returns the response with session cookie.
func login(t *testing.T, h http.Handler, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("password="+password))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// authedGet performs a GET with the session cookie from a fresh login.
func authedGet(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := login(t, h, "pw")
	cookies := rec.Result().Cookies()
	req := httptest.NewRequest("GET", target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	return rec2
}

// bearerGet performs a GET with the admin token as bearer.
func bearerGet(h http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginWrongPasswordRerendersWithError(t *testing.T) {
	s, _ := testServer(t)
	rec := login(t, s.Routes(), "wrong")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 re-render", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Wrong password.") {
		t.Errorf("body missing error line:\n%s", body[:min(len(body), 400)])
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Errorf("session cookie set on failed login")
		}
	}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	s, _ := testServer(t)
	rec := login(t, s.Routes(), "pw")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/feeds" {
		t.Errorf("Location = %q, want /admin/feeds", loc)
	}
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("no session cookie after successful login")
	}
	if !sess.HttpOnly {
		t.Error("session cookie not HttpOnly")
	}
	if !strings.Contains(strings.ToLower(sess.String()), "samesite=lax") && sess.SameSite != http.SameSiteLaxMode {
		t.Error("session cookie SameSite not Lax")
	}
	// The cookie must authenticate the next request.
	req := httptest.NewRequest("GET", "/admin/feeds", nil)
	req.AddCookie(sess)
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("/admin/feeds with session = %d, want 200", rec2.Code)
	}
}

func TestLoginAcceptsAdminTokenAsPassword(t *testing.T) {
	s, _ := testServer(t)
	rec := login(t, s.Routes(), "tok")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (token accepted as password)", rec.Code)
	}
}

func TestAdminUnauthRedirectsToLogin(t *testing.T) {
	s, _ := testServer(t)
	h := s.Routes()
	for _, target := range []string{"/admin/feeds", "/admin/feeds/new", "/admin/rules", "/admin/ondemand"} {
		req := httptest.NewRequest("GET", target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s unauth = %d, want 303", target, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/login" {
			t.Errorf("%s Location = %q, want /login", target, loc)
		}
	}
}

func TestOnDemandUnauthRedirects(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest("GET", "/on-demand?url=https://x.example/feed.xml", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("on-demand unauth = %d, want 303", rec.Code)
	}
}

func TestAPIUnauthReturns401JSON(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest("GET", "/api/feeds", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api unauth = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type = %q, want json", ct)
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Errorf("body = %q, want unauthorized error json", rec.Body.String())
	}
}

func TestBearerTokenWorksOnAPI(t *testing.T) {
	s, _ := testServer(t)
	rec := bearerGet(s.Routes(), "/api/feeds")
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer /api/feeds = %d, want 200", rec.Code)
	}
}

func TestBearerTokenRejectedWhenWrong(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest("GET", "/api/feeds", nil)
	req.Header.Set("Authorization", "Bearer nope")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer = %d, want 401", rec.Code)
	}
}

func TestHealthzOpenToAll(t *testing.T) {
	s, _ := testServer(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestSessionCookieExpiry(t *testing.T) {
	s, _ := testServer(t)
	if s.sessionValid("123.ffffff", time.Now()) {
		t.Error("garbage signature accepted")
	}
	future := time.Now().Add(sessionTTL).Add(time.Hour)
	good := s.signSession(future)
	if !s.sessionValid(good, time.Now()) {
		t.Error("session valid until its expiry was rejected")
	}
	past := s.signSession(time.Now().Add(-time.Minute))
	if s.sessionValid(past, time.Now()) {
		t.Error("expired session accepted")
	}
	if s.sessionValid(s.signSession(time.Now().Add(time.Minute)), time.Now()) != true {
		t.Error("valid session rejected")
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	s, _ := testServer(t)
	h := s.Routes()
	req := httptest.NewRequest("POST", "/logout", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}
}

// seedFeedWithItems creates a feed plus one extracted and one failed item.
func seedFeedWithItems(t *testing.T, st *store.Store) (*model.Feed, model.Item, model.Item) {
	t.Helper()
	ctx := context.Background()
	f := &model.Feed{SourceURL: "https://src.example/feed.xml", Title: "Seed Feed", RefreshEvery: time.Hour}
	if _, err := st.CreateFeed(ctx, f); err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	extracted := &model.Item{
		GUID:        "g-1",
		URL:         "https://src.example/a",
		Title:       "Extracted article",
		Summary:     "short summary",
		ContentHTML: "<p>full text body</p>",
		Extracted:   true,
		PublishedAt: newNullTime(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)),
	}
	if _, err := st.UpsertItem(ctx, f.ID, extracted); err != nil {
		t.Fatalf("UpsertItem extracted: %v", err)
	}
	failed := &model.Item{
		GUID:    "g-2",
		URL:     "https://src.example/b",
		Title:   "Failed article",
		Summary: "fallback summary text",
		Failed:  true,
	}
	if _, err := st.UpsertItem(ctx, f.ID, failed); err != nil {
		t.Fatalf("UpsertItem failed: %v", err)
	}
	if err := st.UpdateItemContent(ctx, failed.ID, "", false, true); err != nil {
		t.Fatalf("UpdateItemContent: %v", err)
	}
	return f, *extracted, *failed
}

func newNullTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: true}
}
