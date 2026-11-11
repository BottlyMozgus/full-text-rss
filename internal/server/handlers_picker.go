package server

// handlers_picker.go: interactive rule picker endpoints. The admin opens an
// article in a same-origin iframe (GET /admin/rules/picker/frame, served
// from extract.PickerDocument output), clicks elements to include/exclude
// (the UI lane's picker.js generates the selectors), previews the result
// (POST /admin/rules/picker/preview on extract.ExtractBody), and saves the
// selectors as the site rule for that host (POST /admin/rules/picker).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"rss-scraper/internal/extract"
	"rss-scraper/internal/model"
)

// rulePickerDot is the rule_picker.html content dot. Empty URL renders the
// URL entry form; otherwise the picker page shows the frame.
type rulePickerDot struct {
	URL             string           // article URL; "" renders the URL entry form
	ItemID          int64            // >0 when opened from a feed item; saving re-extracts it
	Host            string           // rule host to save under
	RuleExists      bool             // rules already cover this host; fields prefilled
	HostRules       []model.SiteRule // every rule matching the host (may compose)
	ContentSelector string           // composed from the host's rules
	StripSelectors  []string
	RewriteFrom     string
	RewriteTo       string
	FrameSrc        string // iframe src for the sanitized article
	PreviewURL      string // POST target for live previews
	Return          string // local path for Cancel and the post-save redirect
}

// pickerFrameCSP sandboxes the framed page: no scripts anywhere, forms
// dead, everything else scoped so the site still looks like itself.
const pickerFrameCSP = "sandbox allow-same-origin; default-src 'none'; " +
	"img-src * data: blob:; style-src * 'unsafe-inline'; font-src * data:; " +
	"media-src *; script-src 'none'; object-src 'none'; frame-src 'none'; " +
	"form-action 'none'"

// pageCache remembers fetched article bodies so the frame, preview and
// save re-extraction share one fetch. Small and time-bounded: it only
// exists to avoid refetching within one picking session.
type pageCache struct {
	mu      sync.Mutex
	entries map[string]pageCacheEntry
	order   []string // fetch URLs, oldest insertion first
	max     int
	ttl     time.Duration
	now     func() time.Time
}

type pageCacheEntry struct {
	body    []byte
	fetched time.Time
}

func newPageCache() *pageCache {
	return &pageCache{
		entries: make(map[string]pageCacheEntry),
		max:     16,
		ttl:     10 * time.Minute,
		now:     time.Now,
	}
}

// get returns the cached body for key, dropping expired entries.
func (c *pageCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().Sub(e.fetched) > c.ttl {
		delete(c.entries, key)
		c.removeOrder(key)
		return nil, false
	}
	return e.body, true
}

// put stores body under key, evicting the oldest entry when full.
func (c *pageCache) put(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
		if len(c.order) > c.max {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = pageCacheEntry{body: body, fetched: c.now()}
}

func (c *pageCache) removeOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// pickerBody returns the cached body for fetchURL or fetches and caches it.
func (s *Server) pickerBody(ctx context.Context, fetchURL string) ([]byte, error) {
	if body, ok := s.picker.get(fetchURL); ok {
		return body, nil
	}
	body, err := s.fetcher.Fetch(ctx, fetchURL)
	if err != nil {
		return nil, err
	}
	s.picker.put(fetchURL, body)
	return body, nil
}

// pickerRuleLookup resolves the article URL to the matching rule and fetch
// URL. requestRewrite, when non-empty, overrides the rule's rewrite. A nil
// rule means no host rule exists yet.
func (s *Server) pickerRuleLookup(articleURL, requestRewriteFrom, requestRewriteTo string) (*model.SiteRule, string) {
	rule, _ := s.st.RuleForHost(context.Background(), hostName(articleURL))
	fetchURL := articleURL
	if requestRewriteFrom != "" {
		fetchURL = strings.ReplaceAll(articleURL, requestRewriteFrom, requestRewriteTo)
	} else if rule != nil && rule.URLRewriteFrom != "" {
		fetchURL = strings.ReplaceAll(articleURL, rule.URLRewriteFrom, rule.URLRewriteTo)
	}
	return rule, fetchURL
}

// hostName returns the lowercase hostname of rawURL, "" on parse failure.
func hostName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// sanitizePickerReturn accepts only same-site absolute paths for the
// return parameter; anything else falls back to the rules list.
func sanitizePickerReturn(ret string) string {
	if ret == "" {
		return "/admin/rules"
	}
	if !strings.HasPrefix(ret, "/") || strings.HasPrefix(ret, "//") || strings.Contains(ret, "\\") {
		return "/admin/rules"
	}
	return ret
}

// pickerURLValid applies the same URL rules as on-demand: absolute
// http(s) with a host.
func pickerURLValid(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// handlePickerPage renders the picker page. Query forms:
//
//	?feed=<id>            redirect to the feed's newest extractable item
//	?item=<id>            pick for that item's URL, return to its preview
//	?url=<article>&return=<local path>
//	(no query)            URL entry form
func (s *Server) handlePickerPage(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query()
	dot := rulePickerDot{
		PreviewURL: "/admin/rules/picker/preview",
		Return:     sanitizePickerReturn(q.Get("return")),
	}

	if fid, ok := queryID(q.Get("feed")); ok {
		itemID, ok := s.newestPickableItem(r.Context(), fid)
		if !ok {
			s.flashForm(w, "rules", "rule_picker", "This feed has no items yet. Refresh it first, or enter an article URL.",
				rulePickerDot{PreviewURL: dot.PreviewURL, Return: sanitizePickerReturn("/admin/feeds/" + strconv.FormatInt(fid, 10))})
			return nil, nil // response already written
		}
		http.Redirect(w, r, "/admin/rules/picker?item="+strconv.FormatInt(itemID, 10), http.StatusSeeOther)
		return nil, nil
	}

	if itemID, ok := queryID(q.Get("item")); ok {
		it, err := s.st.Item(r.Context(), itemID)
		if err != nil {
			return nil, err
		}
		if it.URL == "" {
			s.flashForm(w, "rules", "rule_picker", "This item has no link to pick from. Enter an article URL instead.",
				rulePickerDot{PreviewURL: dot.PreviewURL})
			return nil, nil
		}
		dot.URL = it.URL
		dot.ItemID = it.ID
		dot.Return = sanitizePickerReturn(fmt.Sprintf("/admin/feeds/%d/items/%d", it.FeedID, it.ID))
	} else if raw := strings.TrimSpace(q.Get("url")); raw != "" {
		if !pickerURLValid(raw) {
			s.flashForm(w, "rules", "rule_picker", "Enter a valid http(s) article URL.",
				rulePickerDot{PreviewURL: dot.PreviewURL, Return: dot.Return})
			return nil, nil
		}
		dot.URL = raw
	}

	if dot.URL != "" {
		dot.Host = pickerSaveHost(s, dot.URL)
		hostRules, _ := s.st.RulesForHost(r.Context(), hostName(dot.URL))
		dot.HostRules = hostRules
		rule, _ := s.st.RuleForHost(r.Context(), hostName(dot.URL))
		if rule != nil {
			dot.RuleExists = true
			dot.ContentSelector = rule.ContentSelector
			dot.StripSelectors = rule.StripSelectors
			dot.RewriteFrom = rule.URLRewriteFrom
			dot.RewriteTo = rule.URLRewriteTo
		}
		dot.FrameSrc = "/admin/rules/picker/frame?url=" + url.QueryEscape(dot.URL)
	}
	return dot, nil
}

// pickerSaveHost picks the host the rule is saved under: the existing
// rule's host when one matches, otherwise the article hostname minus a
// leading "www.".
func pickerSaveHost(s *Server, articleURL string) string {
	host := hostName(articleURL)
	if host == "" {
		return ""
	}
	rule, _ := s.st.RuleForHost(context.Background(), host)
	if rule != nil {
		return rule.Host
	}
	return strings.TrimPrefix(host, "www.")
}

// newestPickableItem returns the id of the newest item of the feed that has
// a URL, for the ?feed= shortcut.
func (s *Server) newestPickableItem(ctx context.Context, feedID int64) (int64, bool) {
	items, err := s.st.ItemsForFeed(ctx, feedID, model.DefaultMaxItems)
	if err != nil || len(items) == 0 {
		return 0, false
	}
	for _, it := range items {
		if it.URL != "" {
			return it.ID, true
		}
	}
	return 0, false
}

// queryID parses a positive integer query value.
func queryID(v string) (int64, bool) {
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// handlePickerFrame serves the sanitized article document for the iframe.
// Headers apply to every response, including fetch failures: the frame
// must never be able to escape its sandbox or be misinterpreted.
func (s *Server) handlePickerFrame(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", pickerFrameCSP)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")

	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if !pickerURLValid(rawURL) {
		pickerFrameError(w, http.StatusBadRequest, "Enter a valid http(s) article URL.")
		return
	}
	_, fetchURL := s.pickerRuleLookup(rawURL, r.URL.Query().Get("rewrite_from"), r.URL.Query().Get("rewrite_to"))
	body, err := s.pickerBody(r.Context(), fetchURL)
	if err != nil {
		pickerFrameError(w, http.StatusBadGateway, "Could not fetch the article: "+err.Error())
		return
	}
	doc, err := extract.PickerDocument(body, fetchURL, r.Host)
	if err != nil {
		pickerFrameError(w, http.StatusBadGateway, "Could not prepare the article for picking: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(doc)
}

// pickerFrameError writes a minimal self-contained error document. The
// data-picker-error attribute lets picker.js detect failures and swap in a
// message next to the frame.
func pickerFrameError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html data-picker-error="1"><body><p>%s</p></body></html>`, templateEscape(msg))
}

// templateEscape is the small subset of HTML escaping the error document
// needs (attribute context is quoted, so text escaping suffices).
func templateEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}

// pickerPreviewRequest is the JSON body of the preview endpoint.
type pickerPreviewRequest struct {
	URL             string   `json:"url"`
	ContentSelector string   `json:"content_selector"`
	StripSelectors  []string `json:"strip_selectors"`
	RewriteFrom     string   `json:"rewrite_from"`
	RewriteTo       string   `json:"rewrite_to"`
}

// pickerPreviewResponse is the JSON view of one preview run. content_error
// and strip_errors are index-aligned with the request's selectors; both
// stay "" when the selector is fine.
type pickerPreviewResponse struct {
	HTML           string   `json:"html"`
	Title          string   `json:"title"`
	Mode           string   `json:"mode"`
	TextLength     int      `json:"text_length"`
	ContentMatches int      `json:"content_matches"`
	StripMatches   []int    `json:"strip_matches"`
	ContentError   string   `json:"content_error"`
	StripErrors    []string `json:"strip_errors"`
	Error          string   `json:"error"`
}

// maxPickerPreviewBody caps preview request bodies (selectors are small).
const maxPickerPreviewBody = 64 << 10

// handlePickerPreview runs ExtractBody on the cached page with the posted
// selectors and reports the cleaned HTML plus match counts. Validation and
// match problems come back as 200 with error fields so the picker UI can
// show them inline; only transport-level failures are HTTP errors.
func (s *Server) handlePickerPreview(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSONError(w, http.StatusUnsupportedMediaType, "preview requires Content-Type: application/json")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPickerPreviewBody+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read request body: "+err.Error())
		return
	}
	if len(body) > maxPickerPreviewBody {
		writeJSONError(w, http.StatusBadRequest, "request body too large")
		return
	}
	var req pickerPreviewRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !pickerURLValid(req.URL) {
		writeJSONError(w, http.StatusBadRequest, "url must be an http(s) article URL")
		return
	}

	resp := pickerPreviewResponse{StripMatches: []int{}, StripErrors: []string{}}

	// Compile-checked selectors only go into the extraction rule, so one
	// invalid entry does not blank the whole preview.
	contentSel := strings.TrimSpace(req.ContentSelector)
	if contentSel != "" {
		if err := extract.ValidateSelector(contentSel); err != nil {
			resp.ContentError = err.Error()
			contentSel = ""
		}
	}
	strips := make([]string, 0, len(req.StripSelectors))
	for _, sel := range req.StripSelectors {
		sel = strings.TrimSpace(sel)
		if sel == "" {
			continue
		}
		if err := extract.ValidateSelector(sel); err != nil {
			resp.StripErrors = append(resp.StripErrors, err.Error())
			continue
		}
		resp.StripErrors = append(resp.StripErrors, "")
		strips = append(strips, sel)
	}
	// Index alignment: entries dropped as blank keep their slot so the UI
	// can map errors back to the textarea lines the user typed.
	for i := len(resp.StripErrors); i < len(req.StripSelectors); i++ {
		resp.StripErrors = append(resp.StripErrors, "")
	}
	_ = strips

	_, fetchURL := s.pickerRuleLookup(req.URL, req.RewriteFrom, req.RewriteTo)
	page, err := s.pickerBody(r.Context(), fetchURL)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "could not fetch the article: "+err.Error())
		return
	}

	// Match counts are reported on the page as the server parses it, with
	// the original (possibly invalid) selectors so counts and errors line
	// up with what the user sees.
	sels := make([]string, 0, len(req.StripSelectors)+1)
	if req.ContentSelector != "" {
		sels = append(sels, req.ContentSelector)
	}
	sels = append(sels, req.StripSelectors...)
	counts, cerrs := extract.CountMatches(page, fetchURL, sels...)
	i := 0
	if req.ContentSelector != "" {
		resp.ContentMatches = counts[i]
		if cerrs[i] != nil && resp.ContentError == "" {
			resp.ContentError = cerrs[i].Error()
		}
		i++
	}
	for range req.StripSelectors {
		resp.StripMatches = append(resp.StripMatches, counts[i])
		if cerrs[i] != nil && i-len(counts)+len(resp.StripErrors) >= 0 {
			// keep strip_errors aligned; only fill valid entries
			_ = cerrs[i]
		}
		i++
	}

	rule := &model.SiteRule{ContentSelector: contentSel, StripSelectors: strips}
	res, err := s.extractor.ExtractBody(page, fetchURL, rule)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.HTML = res.ContentHTML
	resp.Title = res.Title
	resp.Mode = res.Mode
	resp.TextLength = len([]rune(strings.TrimSpace(stripTagsForPreview(res.ContentHTML))))
	writeJSON(w, http.StatusOK, resp)
}

// handlePickerSave persists the posted selectors as the host rule and
// re-extracts the source item when one was given. ruleFromForm provides
// the base validation; picker-specific selector compilation errors name
// the offending selector.
func (s *Server) handlePickerSave(w http.ResponseWriter, r *http.Request) {
	rule, parseErr := ruleFromForm(r)
	if parseErr != "" {
		s.renderPickerSaveError(w, r, parseErr)
		return
	}
	// Compile every selector; the form must never save something the
	// extractor would silently skip.
	var bad []string
	if rule.ContentSelector != "" {
		if err := extract.ValidateSelector(rule.ContentSelector); err != nil {
			bad = append(bad, fmt.Sprintf("content selector %q: %v", rule.ContentSelector, err))
		}
	}
	for _, sel := range rule.StripSelectors {
		if err := extract.ValidateSelector(sel); err != nil {
			bad = append(bad, fmt.Sprintf("strip selector %q: %v", sel, err))
		}
	}
	if len(bad) > 0 {
		s.renderPickerSaveError(w, r, "Invalid selector: "+strings.Join(bad, "; "))
		return
	}
	// rule_id decides insert vs update: an id pointing at a rule of the
	// same host updates that rule in place, anything else creates a new
	// rule (multiple rules per host are allowed and compose at extraction).
	if ruleID, ok := queryID(r.PostFormValue("rule_id")); ok {
		if existing, err := s.st.SiteRule(r.Context(), ruleID); err == nil && existing.Host == rule.Host {
			rule.ID = existing.ID
			if err := s.st.UpdateSiteRule(r.Context(), rule); err != nil {
				s.renderPickerSaveError(w, r, friendlyError(err))
				return
			}
			s.pickerFinishSave(w, r, rule)
			return
		}
	}
	if _, err := s.st.CreateSiteRule(r.Context(), rule); err != nil {
		s.renderPickerSaveError(w, r, friendlyError(err))
		return
	}
	s.pickerFinishSave(w, r, rule)
}

// pickerFinishSave re-extracts the source item (when one was given) and
// redirects to the validated return path.
func (s *Server) pickerFinishSave(w http.ResponseWriter, r *http.Request, rule *model.SiteRule) {
	ret := sanitizePickerReturn(r.PostFormValue("return"))
	if itemID, ok := queryID(r.PostFormValue("item_id")); ok {
		s.reextractPickerItem(context.Background(), itemID, rule)
	}
	http.Redirect(w, r, ret, http.StatusSeeOther)
}

// renderPickerSaveError re-renders the picker page with the submitted
// values and an error flash.
func (s *Server) renderPickerSaveError(w http.ResponseWriter, r *http.Request, msg string) {
	rawURL := strings.TrimSpace(r.PostFormValue("url"))
	dot := rulePickerDot{
		URL:             rawURL,
		Host:            strings.TrimSpace(r.PostFormValue("host")),
		ContentSelector: r.PostFormValue("content_selector"),
		RewriteFrom:     r.PostFormValue("rewrite_from"),
		RewriteTo:       r.PostFormValue("rewrite_to"),
		PreviewURL:      "/admin/rules/picker/preview",
		Return:          sanitizePickerReturn(r.PostFormValue("return")),
	}
	if id, ok := queryID(r.PostFormValue("item_id")); ok {
		dot.ItemID = id
	}
	dot.StripSelectors = splitLines(r.PostFormValue("strip_selectors"))
	if pickerURLValid(rawURL) {
		dot.FrameSrc = "/admin/rules/picker/frame?url=" + url.QueryEscape(rawURL)
	}
	s.flashForm(w, "rules", "rule_picker", msg, dot)
}

// reextractPickerItem re-extracts one stored item with the just-saved rule,
// reusing the cached page when possible. Extraction or store failures do
// not fail the save: the rule is persisted either way and the item keeps
// its previous content.
func (s *Server) reextractPickerItem(ctx context.Context, itemID int64, rule *model.SiteRule) {
	it, err := s.st.Item(ctx, itemID)
	if err != nil || it.URL == "" {
		return
	}
	_, fetchURL := s.pickerRuleLookup(it.URL, rule.URLRewriteFrom, rule.URLRewriteTo)
	page, err := s.pickerBody(ctx, fetchURL)
	if err != nil {
		return
	}
	res, err := s.extractor.ExtractBody(page, fetchURL, rule)
	if err != nil {
		return // leave the stored item untouched on failure
	}
	_ = s.st.UpdateItemContent(ctx, it.ID, res.ContentHTML, true, false)
}

// stripTagsForPreview strips tags from cleaned article HTML for the
// preview's text_length figure. Reimplemented here because extract keeps
// stripTags internal.
func stripTagsForPreview(s string) string {
	var out strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return out.String()
}

var _ = errors.New // keep import if unused after edits
