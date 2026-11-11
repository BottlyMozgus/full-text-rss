package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"rss-scraper/internal/model"
)

// intervalOptions are the selectable refresh intervals for the feed form.
// The value is a duration string parseable by time.ParseDuration so the
// form posts exactly what it showed.
var intervalOptions = []struct {
	Value time.Duration
	Label string
}{
	{15 * time.Minute, "15m"},
	{30 * time.Minute, "30m"},
	{time.Hour, "60m"},
	{3 * time.Hour, "3h"},
	{6 * time.Hour, "6h"},
	{12 * time.Hour, "12h"},
	{24 * time.Hour, "24h"},
}

// feedRow is one row of feeds_list.html (see templates README).
type feedRow struct {
	ID            int64
	Title         string
	SourceURL     string
	IntervalLabel string
	LastRefresh   sql.NullTime
	LastStatus    string
	LastError     string
}

// feedsListDot builds the feeds_list content dot.
func feedsListDot(feeds []model.Feed) struct{ Feeds []feedRow } {
	rows := make([]feedRow, 0, len(feeds))
	for i := range feeds {
		f := feeds[i]
		rows = append(rows, feedRow{
			ID:            f.ID,
			Title:         f.Title,
			SourceURL:     f.SourceURL,
			IntervalLabel: intervalLabel(f.RefreshEvery),
			LastRefresh:   f.LastRefresh,
			LastStatus:    f.LastStatus,
			LastError:     f.LastError,
		})
	}
	return struct{ Feeds []feedRow }{Feeds: rows}
}

// friendlyError maps store-level failures to operator-readable text.
func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	return "Something went wrong: " + err.Error()
}

// intervalLabel renders a duration the way the list shows it ("60m", "3h").
func intervalLabel(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Hour {
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	h := d / time.Hour
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(h), 10) + "h"
	}
	// Non-whole hours keep the minutes form.
	return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
}

// handleFeedsList renders the saved feeds table.
func (s *Server) handleFeedsList(w http.ResponseWriter, r *http.Request) (any, error) {
	feeds, err := s.st.Feeds(r.Context())
	if err != nil {
		return nil, err
	}
	return feedsListDot(feeds), nil
}

// feedFormDot is the feed_form.html content dot (new and edit shapes).
type feedFormDot struct {
	Feed struct {
		ID           int64
		SourceURL    string
		Title        string
		RefreshEvery time.Duration
		MaxItems     int
	}
	MaxItemsCeiling int
	FeedURL         string
	IntervalOptions []struct {
		Value time.Duration
		Label string
	}
}

// newFeedFormDot assembles the form dot for a feed (zero ID for new).
func newFeedFormDot(f *model.Feed, baseURL string) feedFormDot {
	dot := feedFormDot{IntervalOptions: intervalOpts(), MaxItemsCeiling: model.MaxItemsCeiling}
	dot.Feed.ID = f.ID
	dot.Feed.SourceURL = f.SourceURL
	dot.Feed.Title = f.Title
	dot.Feed.RefreshEvery = f.RefreshEvery
	dot.Feed.MaxItems = f.MaxItems
	if dot.Feed.MaxItems < 1 {
		dot.Feed.MaxItems = model.DefaultMaxItems // new feed
	}
	if f.ID != 0 && f.Token != "" {
		dot.FeedURL = strings.TrimSuffix(baseURL, "/") + "/feed/" + f.Token + ".xml"
	}
	return dot
}

// intervalOpts converts the package-level table to the anonymous shape the
// template ranges over.
func intervalOpts() []struct {
	Value time.Duration
	Label string
} {
	out := make([]struct {
		Value time.Duration
		Label string
	}, len(intervalOptions))
	for i, o := range intervalOptions {
		out[i] = struct {
			Value time.Duration
			Label string
		}{Value: o.Value, Label: o.Label}
	}
	return out
}

// handleFeedFormNew renders the empty add-feed form.
func (s *Server) handleFeedFormNew(w http.ResponseWriter, r *http.Request) (any, error) {
	return newFeedFormDot(&model.Feed{}, s.baseURL(r)), nil
}

// handleFeedFormEdit renders the edit form incl. the generated feed URL.
func (s *Server) handleFeedFormEdit(w http.ResponseWriter, r *http.Request) (any, error) {
	id, ok := feedPathID(r)
	if !ok {
		return nil, errors.New("bad feed id")
	}
	f, err := s.st.Feed(r.Context(), id)
	if err != nil {
		return nil, err
	}
	return newFeedFormDot(f, s.baseURL(r)), nil
}

// handleFeedCreate parses the new-feed form, creates the feed, and shows
// the edit page whose highlighted feed URL is the action item (AC1).
func (s *Server) handleFeedCreate(w http.ResponseWriter, r *http.Request) {
	f, parseErr := s.feedFromForm(r)
	if parseErr != "" {
		s.flashForm(w, "feeds", "feed_form_new", parseErr, newFeedFormDot(&model.Feed{SourceURL: r.PostFormValue("source_url"), Title: r.PostFormValue("title")}, ""))
		return
	}
	id, err := s.st.CreateFeed(r.Context(), f)
	if err != nil {
		s.flashForm(w, "feeds", "feed_form_new", friendlyError(err), newFeedFormDot(f, ""))
		return
	}
	created, err := s.st.Feed(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/admin/feeds", http.StatusSeeOther)
		return
	}
	s.renderPage(w, renderInput{
		Tmpl: "feed_form_edit",
		Page: Page{
			ActiveNav: "feeds",
			Authed:    true,
			Flash:     "Feed created. Subscribe to the URL below.",
		},
		Content: newFeedFormDot(created, s.baseURL(r)),
	})
}

// handleFeedUpdate applies the edit form.
func (s *Server) handleFeedUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := s.st.Feed(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	updated, parseErr := s.feedFromForm(r)
	if parseErr != "" {
		// Re-show the stored feed with the operator's typed title and URL.
		shown := *f
		shown.SourceURL = r.PostFormValue("source_url")
		shown.Title = r.PostFormValue("title")
		s.flashForm(w, "feeds", "feed_form_edit", parseErr, newFeedFormDot(&shown, s.baseURL(r)))
		return
	}
	f.SourceURL = updated.SourceURL
	f.Title = updated.Title
	f.RefreshEvery = updated.RefreshEvery
	f.MaxItems = updated.MaxItems
	if err := s.st.UpdateFeed(r.Context(), f); err != nil {
		s.flashForm(w, "feeds", "feed_form_edit", friendlyError(err), newFeedFormDot(f, s.baseURL(r)))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/feeds/%d", id), http.StatusSeeOther)
}

// handleFeedDelete removes the feed (items cascade in the store).
func (s *Server) handleFeedDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.st.DeleteFeed(r.Context(), id); err != nil {
		http.Redirect(w, r, "/admin/feeds", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/feeds", http.StatusSeeOther)
}

// feedFromForm validates and maps the feed form fields. The returned string
// is non-empty on validation failure.
func (s *Server) feedFromForm(r *http.Request) (*model.Feed, string) {
	sourceURL := strings.TrimSpace(r.PostFormValue("source_url"))
	title := strings.TrimSpace(r.PostFormValue("title"))
	rawInterval := strings.TrimSpace(r.PostFormValue("refresh_every"))
	if sourceURL == "" {
		return nil, "Source URL is required."
	}
	u, err := url.Parse(sourceURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, "Source URL must be an http(s) feed URL."
	}
	interval, err := time.ParseDuration(rawInterval)
	if err != nil || interval <= 0 {
		return nil, fmt.Sprintf("Refresh interval %q is not a duration like 60m or 3h.", rawInterval)
	}
	// A form posted without the field (older page, script) gets the default.
	maxItems := model.DefaultMaxItems
	if raw := strings.TrimSpace(r.PostFormValue("max_items")); raw != "" {
		maxItems, err = strconv.Atoi(raw)
		if err != nil || maxItems < 1 || maxItems > model.MaxItemsCeiling {
			return nil, fmt.Sprintf("Max items must be a whole number from 1 to %d.", model.MaxItemsCeiling)
		}
	}
	return &model.Feed{SourceURL: sourceURL, Title: title, RefreshEvery: interval, MaxItems: maxItems}, ""
}

// itemRow is one row of feed_detail.html; State is the badge label and
// Details carries the extraction error line.
type itemRow struct {
	ID        int64
	Title     string
	State     string
	Details   string
	FetchedAt sql.NullTime
}

// feedDetailDot is the feed_detail.html content dot.
type feedDetailDot struct {
	Feed struct {
		ID          int64
		Title       string
		SourceURL   string
		LastRefresh sql.NullTime
		LastStatus  string
		LastError   string
	}
	Items []itemRow
}

// itemState maps an item's flags to the badge state label.
func itemState(it model.Item) string {
	switch {
	case it.Extracted:
		return "extracted"
	case it.Failed:
		return "summary-fallback"
	default:
		return "pending"
	}
}

// handleFeedDetail renders status + items for one feed.
func (s *Server) handleFeedDetail(w http.ResponseWriter, r *http.Request) (any, error) {
	id, ok := pathID(r)
	if !ok {
		return nil, errors.New("bad feed id")
	}
	f, err := s.st.Feed(r.Context(), id)
	if err != nil {
		return nil, err
	}
	items, err := s.st.ItemsForFeed(r.Context(), id, 100)
	if err != nil {
		return nil, err
	}
	dot := feedDetailDot{Items: make([]itemRow, 0, len(items))}
	dot.Feed.ID = f.ID
	dot.Feed.Title = f.Title
	dot.Feed.SourceURL = f.SourceURL
	dot.Feed.LastRefresh = f.LastRefresh
	dot.Feed.LastStatus = f.LastStatus
	dot.Feed.LastError = f.LastError
	for i := range items {
		dot.Items = append(dot.Items, itemRow{
			ID:        items[i].ID,
			Title:     items[i].Title,
			State:     itemState(items[i]),
			Details:   itemDetails(items[i]),
			FetchedAt: items[i].FetchedAt,
		})
	}
	return dot, nil
}

// itemDetails builds the small line under the badge: why an item fell back
// to summary, or a hint for pending items.
func itemDetails(it model.Item) string {
	if it.Extracted {
		return ""
	}
	if it.Failed {
		return "Extraction failed; serving the original summary."
	}
	return "Not fetched yet."
}

// itemPreviewDot is the item_preview.html content dot.
type itemPreviewDot struct {
	Feed struct {
		ID    int64
		Title string
	}
	Item struct {
		ID          int64
		Title       string
		URL         string
		Author      string
		ContentHTML template.HTML // rendered pane, pre-sanitized
		RawHTML     string        // code pane, escaped by html/template
	}
}

// handleItemPreview renders the extracted HTML (raw pane) next to its
// escaped source (code pane). The raw pane shows exactly the cleaned HTML
// that lands in content:encoded; content was already sanitized by
// extract.Clean before storage, so rendering it is safe.
func (s *Server) handleItemPreview(w http.ResponseWriter, r *http.Request) (any, error) {
	itemID, ok := pathID(r) // {itemID} is the last segment
	if !ok {
		return nil, errors.New("bad item id")
	}
	it, err := s.st.Item(r.Context(), itemID)
	if err != nil {
		return nil, err
	}
	feed, err := s.st.Feed(r.Context(), it.FeedID)
	if err != nil {
		return nil, err
	}
	var dot itemPreviewDot
	dot.Feed.ID = feed.ID
	dot.Feed.Title = feed.Title
	dot.Item.ID = it.ID
	dot.Item.Title = it.Title
	dot.Item.URL = it.URL
	dot.Item.Author = it.Author
	dot.Item.ContentHTML = template.HTML(it.ContentHTML) // pre-sanitized
	dot.Item.RawHTML = it.ContentHTML                    // escaped in code pane
	return dot, nil
}

// pathID parses the {id}-shaped segment of admin routes where the id is
// the final path segment.
func pathID(r *http.Request) (int64, bool) {
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	last := segs[len(segs)-1]
	id, err := strconv.ParseInt(last, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// idInPath parses the {id} value of routes shaped like /api/<noun>/{id}/<verb>
// where the id is the second-to-last segment.
func idInPath(r *http.Request) (int64, bool) {
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segs) < 2 {
		return 0, false
	}
	id, err := strconv.ParseInt(segs[len(segs)-2], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// feedPathID extracts the feed id for both shapes: .../{id} and .../{id}/<verb>.
func feedPathID(r *http.Request) (int64, bool) {
	if id, ok := idInPath(r); ok {
		return id, true
	}
	return pathID(r)
}

// flashForm re-renders a form page (tmpl names the page define) with a flash error.
func (s *Server) flashForm(w http.ResponseWriter, nav, tmpl, msg string, content any) {
	s.renderPage(w, renderInput{
		Tmpl: tmpl,
		Page: Page{
			ActiveNav:  nav,
			Authed:     true,
			Flash:      msg,
			FlashError: true,
		},
		Content: content,
	})
}

// baseURL returns the externally visible base URL for absolute feed links.
// A configured base (FTRSS_BASE_URL via cfg) wins; otherwise the request's
// scheme/host (httptest and direct LAN access both work).
func (s *Server) baseURL(r *http.Request) string {
	if base := baseURLFromEnv; base != "" {
		return strings.TrimSuffix(base, "/")
	}
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if r.Host == "" {
		return ""
	}
	return scheme + "://" + r.Host
}

// baseURLFromEnv is read once at startup from FTRSS_BASE_URL (empty by
// default). Tests may set it before building the Server.
var baseURLFromEnv = func() string {
	v := os.Getenv("FTRSS_BASE_URL")
	if v == "" {
		return v
	}
	return strings.TrimSuffix(v, "/")
}()

// refreshCtx returns a context for inline refresh work detached from the
// request: refresh may outlive an aborted browser POST.
func refreshCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Minute)
}
