package server

import (
	"errors"
	"net/http"
	"strings"

	"rss-scraper/internal/model"
)

// rulesListDot builds the rules_list content dot (the template consumes
// model.SiteRule fields directly).
func rulesListDot(rules []model.SiteRule) struct{ Rules []model.SiteRule } {
	return struct{ Rules []model.SiteRule }{Rules: rules}
}

// ruleFormDot is the rule_form content dot.
type ruleFormDot struct {
	Rule model.SiteRule
}

// handleRulesList renders the rule table.
func (s *Server) handleRulesList(w http.ResponseWriter, r *http.Request) (any, error) {
	rules, err := s.st.SiteRules(r.Context())
	if err != nil {
		return nil, err
	}
	return rulesListDot(rules), nil
}

// handleRuleFormNew renders the empty add-rule form.
func (s *Server) handleRuleFormNew(w http.ResponseWriter, r *http.Request) (any, error) {
	return ruleFormDot{Rule: model.SiteRule{}}, nil
}

// handleRuleFormEdit renders the edit form for one rule.
func (s *Server) handleRuleFormEdit(w http.ResponseWriter, r *http.Request) (any, error) {
	id, ok := feedPathID(r) // /admin/rules/{id}/edit: id is second-to-last
	if !ok {
		return nil, errors.New("bad rule id")
	}
	rule, err := s.st.SiteRule(r.Context(), id)
	if err != nil {
		return nil, err
	}
	return ruleFormDot{Rule: *rule}, nil
}

// handleRuleCreate parses the add-rule form. Create always inserts a new
// row: multiple rules per host are allowed and compose at extraction time.
func (s *Server) handleRuleCreate(w http.ResponseWriter, r *http.Request) {
	rule, parseErr := ruleFromForm(r)
	if parseErr != "" {
		s.flashForm(w, "rules", "rule_form_new", parseErr, ruleFormDot{Rule: model.SiteRule{Host: strings.TrimSpace(r.PostFormValue("host"))}})
		return
	}
	if _, err := s.st.CreateSiteRule(r.Context(), rule); err != nil {
		s.flashForm(w, "rules", "rule_form_new", friendlyError(err), ruleFormDot{Rule: *rule})
		return
	}
	http.Redirect(w, r, "/admin/rules", http.StatusSeeOther)
}

// handleRuleUpdate applies the edit form, addressed by id. The host may
// change; hosts are no longer unique.
func (s *Server) handleRuleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.st.SiteRule(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	upd, parseErr := ruleFromForm(r)
	if parseErr != "" {
		s.flashForm(w, "rules", "rule_form_edit", parseErr, ruleFormDot{Rule: model.SiteRule{ID: id, Host: strings.TrimSpace(r.PostFormValue("host"))}})
		return
	}
	upd.ID = id
	if err := s.st.UpdateSiteRule(r.Context(), upd); err != nil {
		s.flashForm(w, "rules", "rule_form_edit", friendlyError(err), ruleFormDot{Rule: *upd})
		return
	}
	http.Redirect(w, r, "/admin/rules", http.StatusSeeOther)
}

// handleRuleDelete removes a rule.
func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.st.DeleteSiteRule(r.Context(), id); err != nil {
		s.flashForm(w, "rules", "rules_list", friendlyError(err), rulesListDot(nil))
		return
	}
	http.Redirect(w, r, "/admin/rules", http.StatusSeeOther)
}

// ruleFromForm validates and maps the rule form fields. strip_selectors
// arrives newline-separated; blank lines are dropped. The returned string
// is non-empty on validation failure.
func ruleFromForm(r *http.Request) (*model.SiteRule, string) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.PostFormValue("host")), "."))
	contentSel := strings.TrimSpace(r.PostFormValue("content_selector"))
	strip := splitLines(r.PostFormValue("strip_selectors"))
	rewriteFrom := r.PostFormValue("rewrite_from")
	rewriteTo := r.PostFormValue("rewrite_to")

	if host == "" {
		return nil, "Host is required."
	}
	if rewriteFrom == "" && rewriteTo != "" {
		return nil, "URL rewrite needs both from and to."
	}
	if contentSel == "" && len(strip) == 0 && rewriteFrom == "" {
		return nil, "A rule needs a content selector, strip selectors, or a URL rewrite."
	}
	return &model.SiteRule{
		Host:            host,
		ContentSelector: contentSel,
		StripSelectors:  strip,
		URLRewriteFrom:  rewriteFrom,
		URLRewriteTo:    rewriteTo,
	}, ""
}

// splitLines splits a textarea value into trimmed non-empty lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
