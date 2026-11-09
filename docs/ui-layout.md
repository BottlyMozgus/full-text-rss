# rss-scraper — UI layout notes (for implementation)

Overall feel: dense, utilitarian admin tool (think Miniflux). Light theme only.
System font stack. No framework — plain server-rendered HTML + minimal vanilla JS
(fragments, no SPA). All pages share a slim top bar: product name "Full-Text RSS",
nav links [Feeds] [Rules] [On-demand], and a Logout button when authed.

## Pages

### /login
Single centered column (~360px): h1 "Full-Text RSS", password input, submit
"Sign in". Error text in red below field on failure. No other chrome.

### /admin/feeds (list — default after login)
Table with columns: Title (link to detail), Source URL (truncated, muted),
Interval (e.g. "60m"), Last refresh (relative + status dot: green ok / red
error), Errors (⚠ + count if any), row actions [Refresh] [Edit] [Delete].
"Add feed" button top-right opens /admin/feeds/new.

### /admin/feeds/new + /admin/feeds/<id>/edit
Form fields: Source URL* (text), Title (text, placeholder "auto from feed"),
Refresh interval (select: 15m/30m/60m/3h/6h/12h/24h, default 60m).
On edit: additionally shows the generated feed URL with a Copy button
(`http://<host>/feed/<token>.xml`), and buttons Save / Cancel / Delete.

### /admin/feeds/<id> (detail = status view)
Header: title, source URL, status line (last refresh time + ok/error + error
message if any), buttons [Refresh now] [Edit].
Sections:
1. "Items" table: Title (link to original), State badge
   (extracted=green / summary-fallback=amber / pending=grey), Fetched at.
   Row click opens /admin/feeds/<id>/items/<itemId> preview.
2. Preview page: two-column: rendered cleaned HTML (article) | raw HTML in
   <code> block. Back link.

### /admin/rules (list)
Table: Host, Content selector (code), Strip selectors (count), URL rewrite
("from → to" or —), actions [Edit] [Delete]. "Add rule" top-right.

### /admin/rules/new + /admin/rules/<id>/edit
Fields: Host* (text, e.g. "example.com"), Content selector (text, e.g.
"article.post"), Strip selectors (textarea, one CSS selector per line),
URL rewrite from / to (two text inputs, optional). Save / Cancel / Delete.
Helper text: "Rules override Readability for the given host suffix."

### /admin/ondemand
One field: feed URL + Generate button → navigates to
/on-demand?url=<encoded>, which streams the converted XML (Content-Type
application/rss+xml). Below, a short usage note about the URL pattern for
bookmarking into readers.

## Components / conventions
- Status dot: 8px circle, green #22c55e / red #ef4444 / amber #f59e0b / grey #9ca3af.
- Badges: small pill, 11px uppercase, tinted background (green/amber/grey as above).
- Buttons: default = bordered neutral; primary = solid dark; destructive = solid
  red, always behind a confirm dialog (Delete feed/rule).
- Toast/flash line at top of page after actions ("Feed added", "Refresh started").
- Manual refresh triggers POST + page reload showing updated status; while a
  refresh runs the status dot pulses (CSS animation).
- Empty states: one sentence + action link ("No feeds yet. Add your first feed.").
- No client-side framework; forms post and redirect; <500 lines of JS total.
- WCAG AA contrast; keyboard focus outlines visible; all controls real <button>/<a>.

Copy tone: terse, imperative. No marketing language.
