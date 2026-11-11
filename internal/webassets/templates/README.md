/*
Base layout (layout.html) is used with the server lane's Page wrapper, e.g.:

	type Page struct {
		ActiveNav  string   // "feeds" | "rules" | "ondemand"; "" hides the top bar
		Authed     bool     // false hides the top bar (login flow)
		Flash      string   // "" = no flash bar
		FlashError bool     // true renders the flash as an error variant
		Content    any      // the page-specific dot, passed to the "content" block
	}

Data below is named by the {{define}} inside each file. D is the page dot.
Global funcs expected from the server: relTime(time.Time) string,
statusDot not used directly in markup (dot class logic is inline); if the
server registers statusDot(f model.Feed) string it may replace the inline
{{if eq .LastStatus "ok"}} blocks.

layout.html   base frame. Dot: *Page (fields above). Defines "title" and
              "content" blocks; pages override both.

login.html    "login", rendered WITHOUT base. D: struct {
                  Error string // "" = no error line
              }

feeds_list.html
  "feeds_list"  D: struct {
      Feeds []struct {
          ID            int64
          Title         string
          SourceURL     string
          IntervalLabel string   // "60m", "3h", ...
          LastRefresh   sql.NullTime
          LastStatus    string   // "ok" | "error"
          LastError     string   // shown in the Errors cell title
      }
  }

feed_form.html
  "feed_form_new" / "feed_form_edit"  D: struct {
      Feed struct {
          ID           int64
          SourceURL    string
          Title        string
          RefreshEvery time.Duration
          MaxItems     int // entries served by the feed URL; prefilled 20 on new
      }
      MaxItemsCeiling  int    // upper bound for the max_items input
      FeedURL          string // edit only: /feed/<token>.xml absolute URL
      IntervalOptions  []struct {
          Value time.Duration
          Label string // "15m", "30m", "60m", "3h", "6h", "12h", "24h"
      }
  }
  Both include shared "feed_fields" (fields above).

feed_detail.html
  "feed_detail"  D: struct {
      Feed struct {
          ID          int64
          Title       string
          SourceURL   string
          LastRefresh sql.NullTime
          LastStatus  string // "ok" | "error"
          LastError   string
      }
      Items []model.ItemStatus   // State: "extracted"|"summary-fallback"|"pending";
                                 // add FetchedAt sql.NullTime and ID int64, or
                                 // swap in a server-side view struct with those
  }

item_preview.html
  "item_preview"  D: struct {
      Feed struct { ID int64; Title string }
      Item struct {
          Title       string
          URL         string
          Author      string
          ContentHTML string // rendered raw; also shown escaped in code pane
      }
  }

rules_list.html
  "rules_list"  D: struct {
      Rules []model.SiteRule  // uses Host, ContentSelector, StripSelectors,
                              // URLRewriteFrom, URLRewriteTo, ID
  }

rule_form.html
  "rule_form_new" / "rule_form_edit"  D: struct { Rule model.SiteRule }
  Shared "rule_fields" included by both.

rule_picker.html
  "rule_picker"  D: struct {
      URL             string   // article URL; "" renders the URL entry form
                               // (GET /admin/rules/picker, fields url + return)
      ItemID          int64    // >0 when opened from a feed item (hidden item_id)
      Host            string   // rule host to save under
      RuleExists      bool     // rules cover this host; fields are prefilled
      HostRules       []model.SiteRule // every rule matching the host, for
                                // the "update vs create" select (name rule_id)
      ContentSelector string   // composed view of the host's rules
      StripSelectors  []string
      RewriteFrom     string   // kept as hidden rewrite_from / rewrite_to
      RewriteTo       string
      FrameSrc        string   // iframe src: /admin/rules/picker/frame?url=...
      PreviewURL      string   // POST target for JSON previews
      Return          string   // local path for Back/Cancel and hidden return
  }
  Save is a plain form post to /admin/rules/picker (fields host,
  content_selector, strip_selectors one per line, rewrite_from, rewrite_to,
  url, return, item_id, and optionally rule_id: when rule_id names one of
  the host's rules it is updated in place, anything else creates a new
  rule), so the page works without JavaScript. Multiple rules per host are
  allowed; extraction composes them (strip selectors union, first content
  selector and first URL rewrite win, longest-suffix rules first).

  static/js/picker.js is loaded only by this page (defer). It finds
  [data-picker], the iframe #picker-frame and the save form, then:
    - injects a style tag into the same-origin frame document (outline and
      box-shadow only, classes ftrss-pick-hover / -selected / -excluded);
    - captures clicks in the frame: plain click includes (sets the single
      content selector), Alt/Shift-click or the "Exclude" radio adds a strip
      selector; clicking the same element again removes it;
    - generates short selectors (stable #id, tag.class, tag.class.class, then
      a short tag:nth-of-type chain), checking uniqueness with
      querySelectorAll;
    - keeps the content_selector input and the strip_selectors textarea
      (one per line) in sync with the selector list; editing those fields by
      hand re-marks the frame and refreshes the preview;
    - POSTs JSON {url, content_selector, strip_selectors, rewrite_from,
      rewrite_to} to data-preview-url after changes (debounced) and on the
      Preview button, then shows mode, text_length, match counts, per-selector
      errors and the returned html. The html is inserted as returned: it has
      already passed the server-side Clean pipeline;
    - if the frame document has data-picker-error on <html>, hides the frame
      and shows the message in #picker-frame-msg;
    - Reset restores the prefilled selectors.
  Elements marked data-js-only start hidden and are revealed by the script.

ondemand.html
  "ondemand"  D: struct{} (no fields used)

Form field names the server handlers must read:
  login:   password
  feed:    source_url, title, refresh_every (duration string like "60m"),
           max_items (integer 1..MaxItemsCeiling; missing means 20)
  rule:    host, content_selector, strip_selectors (newline-separated),
           rewrite_from, rewrite_to
  ondemand: url (GET /on-demand)
*/
package templates
