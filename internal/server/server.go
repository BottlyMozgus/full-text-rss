// Package server implements the HTTP layer of full-text-rss: session
// authentication, the admin UI, the JSON API, private token feed delivery,
// and the on-demand feed converter.
package server

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"full-text-rss/internal/config"
	"full-text-rss/internal/extract"
	"full-text-rss/internal/store"
	"full-text-rss/internal/webassets"
)

// Page is the layout dot shape from the templates README. renderPage passes
// its fields to layout.html; the page define named by renderInput.Tmpl
// receives the content dot through the layout's "content" block.
type Page struct {
	ActiveNav  string // "feeds" | "rules" | "ondemand"; "" hides the top bar
	Authed     bool   // false hides the top bar (login flow)
	Flash      string // "" = no flash bar
	FlashError bool   // true renders the flash as an error variant
	Content    any    // the page-specific dot, passed to the "content" block
}

// Refresher runs a full refresh cycle for one feed. The scheduler package
// implements it conceptually; until main wires it in (or the scheduler grows
// a matching RefreshNow), the server falls back to a minimal inline refresh.
type Refresher interface {
	RefreshNow(ctx context.Context, feedID int64) error
}

// Server serves the admin UI, JSON API, token feeds, and on-demand
// conversion. Create with New, optionally attach a scheduler via
// SetRefresher, and serve Routes().
type Server struct {
	cfg       *config.Config
	st        *store.Store
	fetcher   *extract.Fetcher
	extractor *extract.Extractor
	tmpl      *template.Template            // layout + every page; used for login
	pages     map[string]*template.Template // page define name -> base layout bound to it
	refresher Refresher                     // optional; nil = minimal inline refresh
}

// pageDefines lists every page-level {{define}} in templates/*.html. Each one
// gets its own template set whose "content" block renders that define, since
// the layout's "content" block is an empty placeholder.
var pageDefines = []string{
	"feeds_list",
	"feed_form_new",
	"feed_form_edit",
	"feed_detail",
	"item_preview",
	"rules_list",
	"rule_form_new",
	"rule_form_edit",
	"ondemand",
}

// New builds a Server. Templates are parsed once from the embedded assets;
// the fetcher/extractor pair is shared by inline refresh and on-demand.
// The store doubles as the extraction rule source (RuleForHost).
func New(cfg *config.Config, st *store.Store) *Server {
	fetcher := extract.NewFetcher(cfg)
	root := mustParseTemplates()
	return &Server{
		cfg:       cfg,
		st:        st,
		fetcher:   fetcher,
		extractor: extract.NewExtractor(fetcher, st),
		tmpl:      root,
		pages:     mustBindPages(root),
	}
}

// mustBindPages clones the parsed set once per page and binds the layout's
// "content" block to that page's define. Clones are made before any
// execution, so the block override is legal.
func mustBindPages(root *template.Template) map[string]*template.Template {
	out := make(map[string]*template.Template, len(pageDefines))
	for _, name := range pageDefines {
		t, err := root.Clone()
		if err != nil {
			panic("server: clone templates: " + err.Error())
		}
		src := `{{define "content"}}{{template "` + name + `" .Content}}{{end}}`
		if _, err := t.Parse(src); err != nil {
			panic("server: bind page " + name + ": " + err.Error())
		}
		out[name] = t
	}
	return out
}

// SetRefresher attaches the scheduler used by manual refresh endpoints.
// Without one, refresh runs inline in the request goroutine.
func (s *Server) SetRefresher(r Refresher) { s.refresher = r }

// mustParseTemplates parses every embedded template with layout.html first
// so that its {{block "title"}}/{{block "content"}} placeholders resolve to
// the page-defined overrides (html/template keeps the FIRST parsed
// definition of a block name; pages parsed after layout therefore replace
// the defaults, and page defines do not collide because each template's
// namespace inherits the set).
func mustParseTemplates() *template.Template {
	root := template.New("layout.html").Funcs(template.FuncMap{
		"relTime": relTime,
	})
	layout, err := fs.ReadFile(webassets.TemplatesFS, "templates/layout.html")
	if err != nil {
		panic("server: read layout.html: " + err.Error())
	}
	if _, err := root.Parse(string(layout)); err != nil {
		panic("server: parse layout.html: " + err.Error())
	}
	pages, err := fs.Glob(webassets.TemplatesFS, "templates/*.html")
	if err != nil {
		panic("server: glob templates: " + err.Error())
	}
	for _, name := range pages {
		if name == "templates/layout.html" {
			continue
		}
		b, err := fs.ReadFile(webassets.TemplatesFS, name)
		if err != nil {
			panic("server: read " + name + ": " + err.Error())
		}
		if _, err := root.New(name).Parse(string(b)); err != nil {
			panic("server: parse " + name + ": " + err.Error())
		}
	}
	return root
}

// Routes builds the full mux with auth middleware.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated endpoints.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.Handle("GET /static/", staticHandler())

	// Private token feeds: the token IS the auth, so they bypass the auth
	// middleware entirely.
	mux.HandleFunc("GET /feed/{token}", s.handleFeedToken)

	// On-demand: authenticated (session cookie or bearer token).
	mux.Handle("GET /on-demand", s.auth(s.handleOnDemand))

	// Admin UI. Content handlers return the template dot; s.page wraps them
	// in the base layout.
	mux.Handle("GET /admin/feeds", s.auth(s.page(s.handleFeedsList, "feeds", "feeds_list")))
	mux.Handle("GET /admin/feeds/new", s.auth(s.page(s.handleFeedFormNew, "feeds", "feed_form_new")))
	mux.Handle("POST /admin/feeds/new", s.auth(s.handleFeedCreate))
	mux.Handle("GET /admin/feeds/{id}", s.auth(s.page(s.handleFeedDetail, "feeds", "feed_detail")))
	mux.Handle("GET /admin/feeds/{id}/edit", s.auth(s.page(s.handleFeedFormEdit, "feeds", "feed_form_edit")))
	mux.Handle("POST /admin/feeds/{id}/edit", s.auth(s.handleFeedUpdate))
	mux.Handle("POST /admin/feeds/{id}/delete", s.auth(s.handleFeedDelete))
	mux.Handle("POST /admin/feeds/{id}/refresh", s.auth(s.handleFeedRefresh))
	mux.Handle("GET /admin/feeds/{id}/items/{itemID}", s.auth(s.page(s.handleItemPreview, "feeds", "item_preview")))
	mux.Handle("GET /admin/rules", s.auth(s.page(s.handleRulesList, "rules", "rules_list")))
	mux.Handle("GET /admin/rules/new", s.auth(s.page(s.handleRuleFormNew, "rules", "rule_form_new")))
	mux.Handle("POST /admin/rules/new", s.auth(s.handleRuleCreate))
	mux.Handle("GET /admin/rules/{id}/edit", s.auth(s.page(s.handleRuleFormEdit, "rules", "rule_form_edit")))
	mux.Handle("POST /admin/rules/{id}/edit", s.auth(s.handleRuleUpdate))
	mux.Handle("POST /admin/rules/{id}/delete", s.auth(s.handleRuleDelete))
	mux.Handle("GET /admin/ondemand", s.auth(s.page(s.handleOnDemandPage, "ondemand", "ondemand")))

	// JSON API. Bearer-token auth is accepted in addition to the session
	// cookie; authAPI answers 401 JSON instead of redirecting browsers.
	mux.Handle("GET /api/feeds", s.authAPI(s.apiListFeeds))
	mux.Handle("POST /api/feeds", s.authAPI(s.apiCreateFeed))
	mux.Handle("GET /api/feeds/{id}", s.authAPI(s.apiGetFeed))
	mux.Handle("PUT /api/feeds/{id}", s.authAPI(s.apiUpdateFeed))
	mux.Handle("DELETE /api/feeds/{id}", s.authAPI(s.apiDeleteFeed))
	mux.Handle("POST /api/feeds/{id}/refresh", s.authAPI(s.apiRefreshFeed))
	mux.Handle("GET /api/feeds/{id}/status", s.authAPI(s.apiFeedStatus))
	mux.Handle("GET /api/rules", s.authAPI(s.apiListRules))
	mux.Handle("POST /api/rules", s.authAPI(s.apiCreateRule))
	mux.Handle("GET /api/rules/{id}", s.authAPI(s.apiGetRule))
	mux.Handle("PUT /api/rules/{id}", s.authAPI(s.apiUpdateRule))
	mux.Handle("DELETE /api/rules/{id}", s.authAPI(s.apiDeleteRule))

	return mux
}

// staticHandler serves the embedded static assets at /static/.
func staticHandler() http.Handler {
	sub, err := fs.Sub(webassets.StaticFS, "static")
	if err != nil {
		panic("server: static sub fs: " + err.Error())
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// page wraps a content handler in the base layout. nav selects the active
// top-bar entry; tmpl names the page define to render (a key of pageDefines).
// The content dot is handed to that define via the layout's "content" block.
func (s *Server) page(content func(http.ResponseWriter, *http.Request) (any, error), nav, tmpl string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dot, err := content(w, r)
		if err != nil {
			// Read failures surface as a flash error on the relevant page.
			s.renderPage(w, renderInput{
				Tmpl: tmpl,
				Page: Page{
					ActiveNav:  nav,
					Authed:     true,
					Flash:      fmt.Sprintf("Something went wrong: %v", err),
					FlashError: true,
				},
				Content: s.fallbackContent(r, nav),
			})
			return
		}
		s.renderPage(w, renderInput{
			Tmpl:    tmpl,
			Page:    Page{ActiveNav: nav, Authed: true},
			Content: dot,
		})
	}
}

// renderInput pairs the layout dot (Page) with the page content dot and the
// page define name (Tmpl, one of pageDefines).
type renderInput struct {
	Tmpl    string
	Page    Page
	Content any
}

// renderPage executes the base layout bound to the requested page. Page
// drives the frame (nav, auth, flash); the page define receives p.Content as
// its dot so templates read their fields directly ({{range .Feeds}} etc.).
func (s *Server) renderPage(w http.ResponseWriter, in renderInput) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	set, ok := s.pages[in.Tmpl]
	if !ok {
		http.Error(w, "unknown page "+in.Tmpl, http.StatusInternalServerError)
		return
	}
	if err := set.ExecuteTemplate(w, "base", map[string]any{
		"ActiveNav":  in.Page.ActiveNav,
		"Authed":     in.Page.Authed,
		"Flash":      in.Page.Flash,
		"FlashError": in.Page.FlashError,
		"Content":    in.Content,
	}); err != nil {
		// Headers may already be gone; the visitor sees a truncated page.
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// fallbackContent rebuilds a list dot for error renders so the flash bar
// page still shows content. For detail/form pages an empty struct keeps the
// error flash alone on the page.
func (s *Server) fallbackContent(r *http.Request, nav string) any {
	switch nav {
	case "feeds":
		if feeds, err := s.st.Feeds(r.Context()); err == nil {
			return feedsListDot(feeds)
		}
	case "rules":
		if rules, err := s.st.SiteRules(r.Context()); err == nil {
			return rulesListDot(rules)
		}
	}
	return struct{}{}
}
