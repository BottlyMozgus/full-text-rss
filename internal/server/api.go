package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"rss-scraper/internal/model"
)

// JSON shapes mirror the model types; durations are plain seconds so the
// API stays dependency-free for curl/jq consumers.

// feedJSON is the API view of model.Feed.
type feedJSON struct {
	ID           int64      `json:"id"`
	Token        string     `json:"token"`
	FeedURL      string     `json:"feed_url,omitempty"` // absolute when derivable
	SourceURL    string     `json:"source_url"`
	Title        string     `json:"title"`
	RefreshEvery int64      `json:"refresh_every_seconds"`
	MaxItems     int        `json:"max_items"`
	LastRefresh  *time.Time `json:"last_refresh,omitempty"`
	LastStatus   string     `json:"last_status"`
	LastError    string     `json:"last_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// ruleJSON is the API view of model.SiteRule.
type ruleJSON struct {
	ID              int64     `json:"id"`
	Host            string    `json:"host"`
	ContentSelector string    `json:"content_selector"`
	StripSelectors  []string  `json:"strip_selectors"`
	URLRewriteFrom  string    `json:"url_rewrite_from,omitempty"`
	URLRewriteTo    string    `json:"url_rewrite_to,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// itemStatusJSON is the API view of model.ItemStatus plus the item id.
type itemStatusJSON struct {
	ID      int64  `json:"id"`
	GUID    string `json:"guid"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Details string `json:"details,omitempty"`
}

// feedStatusJSON is GET /api/feeds/{id}/status.
type feedStatusJSON struct {
	Feed        feedJSON         `json:"feed"`
	Items       []itemStatusJSON `json:"items"`
	PendingIDs  []int64          `json:"pending_item_ids"`
	LastRefresh *time.Time       `json:"last_refresh,omitempty"`
	LastStatus  string           `json:"last_status"`
	LastError   string           `json:"last_error,omitempty"`
}

// toFeedJSON maps a model.Feed; base gives absolute feed_url links.
func toFeedJSON(f *model.Feed, base string) feedJSON {
	j := feedJSON{
		ID:           f.ID,
		Token:        f.Token,
		SourceURL:    f.SourceURL,
		Title:        f.Title,
		RefreshEvery: int64(f.RefreshEvery / time.Second),
		MaxItems:     f.MaxItems,
		LastStatus:   f.LastStatus,
		LastError:    f.LastError,
		CreatedAt:    f.CreatedAt,
	}
	if f.LastRefresh.Valid {
		t := f.LastRefresh.Time
		j.LastRefresh = &t
	}
	if base != "" && f.Token != "" {
		j.FeedURL = strings.TrimSuffix(base, "/") + "/feed/" + f.Token + ".xml"
	}
	return j
}

// toRuleJSON maps a model.SiteRule.
func toRuleJSON(r *model.SiteRule) ruleJSON {
	return ruleJSON{
		ID:              r.ID,
		Host:            r.Host,
		ContentSelector: r.ContentSelector,
		StripSelectors:  r.StripSelectors,
		URLRewriteFrom:  r.URLRewriteFrom,
		URLRewriteTo:    r.URLRewriteTo,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

// writeJSON writes v as JSON with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// writeJSONError writes {"error": msg} with status.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// apiBase returns the absolute-URL base for API feed links (request-derived;
// the API has no request when base URL is configured).
func (s *Server) apiBase(r *http.Request) string { return s.baseURL(r) }

// apiListFeeds handles GET /api/feeds.
func (s *Server) apiListFeeds(w http.ResponseWriter, r *http.Request) {
	feeds, err := s.st.Feeds(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	base := s.apiBase(r)
	out := make([]feedJSON, 0, len(feeds))
	for i := range feeds {
		out = append(out, toFeedJSON(&feeds[i], base))
	}
	writeJSON(w, http.StatusOK, out)
}

// defaultRefreshSeconds is the spec default (60 minutes) used when a client
// omits refresh_every_seconds.
const defaultRefreshSeconds int64 = 3600

// feedInput is the accepted create/update payload. RefreshEverySeconds
// defaults to 60 minutes when omitted or zero; negative values are rejected.
// MaxItems omitted or zero means "not given": create uses
// model.DefaultMaxItems and update keeps the stored value. Title optional.
type feedInput struct {
	SourceURL           string `json:"source_url"`
	Title               string `json:"title"`
	RefreshEverySeconds int64  `json:"refresh_every_seconds"`
	MaxItems            int    `json:"max_items"`
}

// applyFeedInput validates input into a model.Feed; "" return is an error
// message for the client.
func applyFeedInput(in feedInput) (*model.Feed, string) {
	if strings.TrimSpace(in.SourceURL) == "" {
		return nil, "source_url is required"
	}
	if in.RefreshEverySeconds < 0 {
		return nil, "refresh_every_seconds must not be negative"
	}
	if in.RefreshEverySeconds == 0 {
		in.RefreshEverySeconds = defaultRefreshSeconds
	}
	if in.MaxItems < 0 || in.MaxItems > model.MaxItemsCeiling {
		return nil, fmt.Sprintf("max_items must be between 1 and %d", model.MaxItemsCeiling)
	}
	return &model.Feed{
		SourceURL:    strings.TrimSpace(in.SourceURL),
		Title:        strings.TrimSpace(in.Title),
		RefreshEvery: time.Duration(in.RefreshEverySeconds) * time.Second,
		MaxItems:     in.MaxItems,
	}, ""
}

// apiCreateFeed handles POST /api/feeds.
func (s *Server) apiCreateFeed(w http.ResponseWriter, r *http.Request) {
	var in feedInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	f, msg := applyFeedInput(in)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	id, err := s.st.CreateFeed(r.Context(), f)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	created, err := s.st.Feed(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toFeedJSON(created, s.apiBase(r)))
}

// feedOr404 loads the {id} feed, writing the right error on failure.
func (s *Server) feedOr404(w http.ResponseWriter, r *http.Request) (*model.Feed, bool) {
	id, ok := feedPathID(r)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "feed not found")
		return nil, false
	}
	f, err := s.st.Feed(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "feed not found")
		return nil, false
	}
	return f, true
}

// apiGetFeed handles GET /api/feeds/{id}.
func (s *Server) apiGetFeed(w http.ResponseWriter, r *http.Request) {
	f, ok := s.feedOr404(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toFeedJSON(f, s.apiBase(r)))
}

// apiUpdateFeed handles PUT /api/feeds/{id}.
func (s *Server) apiUpdateFeed(w http.ResponseWriter, r *http.Request) {
	f, ok := s.feedOr404(w, r)
	if !ok {
		return
	}
	var in feedInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	upd, msg := applyFeedInput(in)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	f.SourceURL = upd.SourceURL
	f.Title = upd.Title
	f.RefreshEvery = upd.RefreshEvery
	if upd.MaxItems != 0 {
		f.MaxItems = upd.MaxItems
	}
	if err := s.st.UpdateFeed(r.Context(), f); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toFeedJSON(f, s.apiBase(r)))
}

// apiDeleteFeed handles DELETE /api/feeds/{id}.
func (s *Server) apiDeleteFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "feed not found")
		return
	}
	if err := s.st.DeleteFeed(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "feed not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// apiRefreshFeed handles POST /api/feeds/{id}/refresh. The cycle runs
// synchronously so the response reflects the outcome; the scheduler is used
// when wired in.
func (s *Server) apiRefreshFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "feed not found")
		return
	}
	if _, err := s.st.Feed(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusNotFound, "feed not found")
		return
	}
	var err error
	if s.refresher != nil {
		err = s.refresher.RefreshNow(r.Context(), id)
	} else {
		ctx, cancel := refreshCtx()
		defer cancel()
		err = s.inlineRefresh(ctx, id)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"refreshed": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"refreshed": true})
}

// apiFeedStatus handles GET /api/feeds/{id}/status.
func (s *Server) apiFeedStatus(w http.ResponseWriter, r *http.Request) {
	f, ok := s.feedOr404(w, r)
	if !ok {
		return
	}
	items, err := s.st.ItemsForFeed(r.Context(), f.ID, 100)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := feedStatusJSON{
		Feed:        toFeedJSON(f, s.apiBase(r)),
		Items:       make([]itemStatusJSON, 0, len(items)),
		PendingIDs:  []int64{},
		LastRefresh: toFeedJSON(f, "").LastRefresh,
		LastStatus:  f.LastStatus,
		LastError:   f.LastError,
	}
	for i := range items {
		it := items[i]
		out.Items = append(out.Items, itemStatusJSON{
			ID:      it.ID,
			GUID:    it.GUID,
			Title:   it.Title,
			State:   itemState(it),
			Details: itemDetails(it),
		})
		if !it.Extracted {
			out.PendingIDs = append(out.PendingIDs, it.ID)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ruleInput is the accepted create/update payload.
type ruleInput struct {
	Host            string   `json:"host"`
	ContentSelector string   `json:"content_selector"`
	StripSelectors  []string `json:"strip_selectors"`
	URLRewriteFrom  string   `json:"url_rewrite_from"`
	URLRewriteTo    string   `json:"url_rewrite_to"`
}

// applyRuleInput validates input into a model.SiteRule; "" return is an
// error message for the client.
func applyRuleInput(in ruleInput) (*model.SiteRule, string) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.Host), "."))
	if host == "" {
		return nil, "host is required"
	}
	if in.URLRewriteFrom == "" && in.URLRewriteTo != "" {
		return nil, "url_rewrite_to requires url_rewrite_from"
	}
	return &model.SiteRule{
		Host:            host,
		ContentSelector: strings.TrimSpace(in.ContentSelector),
		StripSelectors:  in.StripSelectors,
		URLRewriteFrom:  in.URLRewriteFrom,
		URLRewriteTo:    in.URLRewriteTo,
	}, ""
}

// apiListRules handles GET /api/rules.
func (s *Server) apiListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.st.SiteRules(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]ruleJSON, 0, len(rules))
	for i := range rules {
		out = append(out, toRuleJSON(&rules[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// apiCreateRule handles POST /api/rules.
func (s *Server) apiCreateRule(w http.ResponseWriter, r *http.Request) {
	var in ruleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	rule, msg := applyRuleInput(in)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	id, err := s.st.UpsertSiteRule(r.Context(), rule)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved, err := s.st.SiteRule(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toRuleJSON(saved))
}

// ruleOr404 loads the {id} rule, writing the right error on failure.
func (s *Server) ruleOr404(w http.ResponseWriter, r *http.Request) (*model.SiteRule, bool) {
	id, ok := feedPathID(r)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "rule not found")
		return nil, false
	}
	rule, err := s.st.SiteRule(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "rule not found")
		return nil, false
	}
	return rule, true
}

// apiGetRule handles GET /api/rules/{id}.
func (s *Server) apiGetRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := s.ruleOr404(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toRuleJSON(rule))
}

// apiUpdateRule handles PUT /api/rules/{id}.
func (s *Server) apiUpdateRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := s.ruleOr404(w, r)
	if !ok {
		return
	}
	var in ruleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	upd, msg := applyRuleInput(in)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	// The store has no UpdateSiteRule: rules are unique by host and
	// UpsertSiteRule is the update path. A host edit means a new row plus
	// deleting the old one; same-host edits upsert in place.
	if rule.Host != upd.Host {
		upd.ID = 0
		if _, err := s.st.UpsertSiteRule(r.Context(), upd); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.st.DeleteSiteRule(r.Context(), rule.ID); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		saved, err := s.st.RuleForHost(r.Context(), upd.Host)
		if err != nil || saved == nil {
			writeJSONError(w, http.StatusInternalServerError, "updated rule not found")
			return
		}
		writeJSON(w, http.StatusOK, toRuleJSON(saved))
		return
	}
	upd.ID = rule.ID
	if _, err := s.st.UpsertSiteRule(r.Context(), upd); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved, err := s.st.SiteRule(r.Context(), rule.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRuleJSON(saved))
}

// apiDeleteRule handles DELETE /api/rules/{id}.
func (s *Server) apiDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "rule not found")
		return
	}
	if err := s.st.DeleteSiteRule(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "rule not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
