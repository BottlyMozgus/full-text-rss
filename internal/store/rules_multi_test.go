package store

// rules_multi_test.go: multiple rules per host and their composition.

import (
	"context"
	"testing"

	"rss-scraper/internal/model"
)

func TestMultipleRulesPerHostCompose(t *testing.T) {
	path := t.TempDir() + "/multi.db"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// Two rules for the same host, plus one for a suffix host.
	broad := &model.SiteRule{Host: "example.com", ContentSelector: "article", StripSelectors: []string{".ads"}}
	if _, err := s.CreateSiteRule(ctx, broad); err != nil {
		t.Fatalf("CreateSiteRule broad: %v", err)
	}
	exact := &model.SiteRule{Host: "www.example.com", StripSelectors: []string{".share", ".ads2"}, URLRewriteFrom: "/amp", URLRewriteTo: "/"}
	if _, err := s.CreateSiteRule(ctx, exact); err != nil {
		t.Fatalf("CreateSiteRule exact: %v", err)
	}

	rules, err := s.RulesForHost(ctx, "www.example.com")
	if err != nil || len(rules) != 2 {
		t.Fatalf("RulesForHost = %v err %v, want 2 rules", rules, err)
	}
	// Longest suffix first: the exact rule leads.
	if rules[0].Host != "www.example.com" || rules[1].Host != "example.com" {
		t.Errorf("rule order = %s then %s, want exact first", rules[0].Host, rules[1].Host)
	}

	composed, err := s.RuleForHost(ctx, "www.example.com")
	if err != nil || composed == nil {
		t.Fatalf("RuleForHost = %v err %v, want composed", composed, err)
	}
	// Content selector: first rule in order that has one.
	if composed.ContentSelector != "article" {
		t.Errorf("composed content selector = %q", composed.ContentSelector)
	}
	// Strip selectors: union in rule order.
	if len(composed.StripSelectors) != 3 || composed.StripSelectors[0] != ".share" || composed.StripSelectors[2] != ".ads" {
		t.Errorf("composed strip = %v, want [.share .ads2 .ads]", composed.StripSelectors)
	}
	// First URL rewrite wins.
	if composed.URLRewriteFrom != "/amp" || composed.URLRewriteTo != "/" {
		t.Errorf("composed rewrite = %q -> %q", composed.URLRewriteFrom, composed.URLRewriteTo)
	}

	// Suffix matching still works for deeper hosts.
	composed, err = s.RuleForHost(ctx, "deep.www.example.com")
	if err != nil || composed == nil || len(composed.StripSelectors) != 3 {
		t.Errorf("deep host composition = %+v err %v", composed, err)
	}

	// A third exact rule updates nothing existing; three rules now.
	third := &model.SiteRule{Host: "example.com", ContentSelector: "div.main"}
	if _, err := s.CreateSiteRule(ctx, third); err != nil {
		t.Fatalf("CreateSiteRule third: %v", err)
	}
	composed, _ = s.RuleForHost(ctx, "www.example.com")
	if composed.ContentSelector != "article" {
		t.Errorf("content selector after third rule = %q, want first non-empty in order", composed.ContentSelector)
	}

	// UpsertSiteRule keeps updating the first exact rule for the host.
	upd := &model.SiteRule{Host: "example.com", StripSelectors: []string{".x"}}
	if _, err := s.UpsertSiteRule(ctx, upd); err != nil {
		t.Fatalf("UpsertSiteRule: %v", err)
	}
	all, _ := s.SiteRules(ctx)
	if len(all) != 3 {
		t.Errorf("rules after upsert = %d, want 3 (no new row)", len(all))
	}
}
