package extract

// rules.go: helper types for rule handling shared by extractor.go.

import (
	"github.com/andybalholm/cascadia"
)

// cascadiaSafe compiles a CSS selector, returning (nil, err) instead of
// panicking on invalid input so user-managed rules cannot crash extraction.
func cascadiaSafe(sel string) (cascadia.Selector, error) {
	if sel == "" {
		return nil, nil
	}
	m, err := cascadia.Compile(sel)
	if err != nil {
		return nil, err
	}
	return m, nil
}
