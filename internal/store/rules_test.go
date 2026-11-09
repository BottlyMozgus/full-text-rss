package store

import (
	"context"
	"database/sql"
	"testing"

	"full-text-rss/internal/model"
)

func TestUpsertSiteRuleInsertAndUpdate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	r := &model.SiteRule{
		Host:            "example.com",
		ContentSelector: "article.main",
		StripSelectors:  []string{"aside", ".ads"},
		URLRewriteFrom:  "http://",
		URLRewriteTo:    "https://",
	}
	id, err := s.UpsertSiteRule(ctx, r)
	if err != nil {
		t.Fatalf("UpsertSiteRule: %v", err)
	}
	if id <= 0 || r.ID != id {
		t.Fatalf("id = %d, r.ID = %d", id, r.ID)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not set: %+v", r)
	}
	firstCreated := r.CreatedAt

	// Same host again -> update, not a second row.
	r2 := &model.SiteRule{
		Host:            "example.com",
		ContentSelector: "#content",
		StripSelectors:  []string{"nav"},
	}
	id2, err := s.UpsertSiteRule(ctx, r2)
	if err != nil {
		t.Fatalf("UpsertSiteRule second: %v", err)
	}
	if id2 != id {
		t.Fatalf("second upsert id = %d, want same %d", id2, id)
	}
	rules, err := s.SiteRules(ctx)
	if err != nil {
		t.Fatalf("SiteRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("SiteRules len = %d, want 1 (unique by host)", len(rules))
	}
	got := rules[0]
	if got.ContentSelector != "#content" {
		t.Fatalf("ContentSelector = %q, want updated value", got.ContentSelector)
	}
	if len(got.StripSelectors) != 1 || got.StripSelectors[0] != "nav" {
		t.Fatalf("StripSelectors = %v, want [nav]", got.StripSelectors)
	}
	if !got.CreatedAt.Equal(firstCreated) {
		t.Fatalf("CreatedAt changed on update: %v -> %v", firstCreated, got.CreatedAt)
	}

	// Strip selectors with empty list survive the round trip.
	r3 := &model.SiteRule{Host: "plain.example", ContentSelector: "p"}
	if _, err := s.UpsertSiteRule(ctx, r3); err != nil {
		t.Fatalf("UpsertSiteRule r3: %v", err)
	}
	gotRule, err := s.SiteRule(ctx, r3.ID)
	if err != nil {
		t.Fatalf("SiteRule: %v", err)
	}
	if gotRule.StripSelectors != nil {
		t.Fatalf("StripSelectors = %v, want nil", gotRule.StripSelectors)
	}
}

func TestSiteRuleCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	r := &model.SiteRule{Host: "a.com", ContentSelector: "article"}
	id, err := s.UpsertSiteRule(ctx, r)
	if err != nil {
		t.Fatalf("UpsertSiteRule: %v", err)
	}
	got, err := s.SiteRule(ctx, id)
	if err != nil {
		t.Fatalf("SiteRule: %v", err)
	}
	if got.Host != "a.com" {
		t.Fatalf("Host = %q", got.Host)
	}
	if err := s.DeleteSiteRule(ctx, id); err != nil {
		t.Fatalf("DeleteSiteRule: %v", err)
	}
	if _, err := s.SiteRule(ctx, id); err != sql.ErrNoRows {
		t.Fatalf("SiteRule after delete: err = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteSiteRule(ctx, id); err != sql.ErrNoRows {
		t.Fatalf("DeleteSiteRule missing: err = %v, want sql.ErrNoRows", err)
	}
}

func TestRuleForHostSuffixMatching(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fixtures := []model.SiteRule{
		{Host: "example.com", ContentSelector: "root-rule"},
		{Host: "deep.sub.example.com", ContentSelector: "deep-rule"},
		{Host: "other.org", ContentSelector: "other-rule"},
	}
	for i := range fixtures {
		if _, err := s.UpsertSiteRule(ctx, &fixtures[i]); err != nil {
			t.Fatalf("UpsertSiteRule %s: %v", fixtures[i].Host, err)
		}
	}

	tests := []struct {
		host string
		want string // "" -> nil
	}{
		{host: "example.com", want: "root-rule"},            // exact
		{host: "www.example.com", want: "root-rule"},        // suffix
		{host: "deep.sub.example.com", want: "deep-rule"},   // exact beats suffix
		{host: "x.deep.sub.example.com", want: "deep-rule"}, // longest suffix wins
		{host: "notexample.com", want: ""},                  // suffix must be dot-delimited
		{host: "example.com.evil.net", want: ""},            // no reverse suffix
		{host: "other.org", want: "other-rule"},             // exact
		{host: "www.other.org", want: "other-rule"},         // suffix
		{host: "unrelated.net", want: ""},                   // no match
	}
	for _, tt := range tests {
		got, err := s.RuleForHost(ctx, tt.host)
		if err != nil {
			t.Fatalf("RuleForHost(%q): %v", tt.host, err)
		}
		if tt.want == "" {
			if got != nil {
				t.Fatalf("RuleForHost(%q) = %s, want nil", tt.host, got.ContentSelector)
			}
			continue
		}
		if got == nil || got.ContentSelector != tt.want {
			t.Fatalf("RuleForHost(%q) = %v, want rule %q", tt.host, got, tt.want)
		}
	}
}

func TestRuleForHostEmpty(t *testing.T) {
	s := openTestStore(t)
	got, err := s.RuleForHost(context.Background(), "")
	if err != nil {
		t.Fatalf("RuleForHost empty: %v", err)
	}
	if got != nil {
		t.Fatalf("RuleForHost empty = %+v, want nil", got)
	}
}
