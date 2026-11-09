package templates

import (
	"html/template"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTemplatesParse keeps markup honest: every template file must parse.
func TestTemplatesParse(t *testing.T) {
	files, err := filepath.Glob("*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (files=%d)", err, len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if _, err := template.New(f).Funcs(template.FuncMap{"relTime": func(t time.Time) string { return "" }, "statusDot": func(s string) string { return s }}).Parse(string(b)); err != nil {
			t.Errorf("%s: parse error: %v", f, err)
		}
	}
}
