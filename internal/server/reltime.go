package server

import (
	"fmt"
	"time"
)

// relTime renders a timestamp the way the admin UI shows it: "never" for
// zero values, then coarse buckets ("45s ago", "3m ago", "2h ago",
// "5d ago"). Past times get "ago"; future times render "in ..." form.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	if d < 0 {
		d = -d
		switch {
		case d < time.Minute:
			return "soon"
		case d < time.Hour:
			return fmt.Sprintf("in %dm", int(d/time.Minute))
		case d < 24*time.Hour:
			return fmt.Sprintf("in %dh", int(d/time.Hour))
		default:
			return fmt.Sprintf("in %dd", int(d/(24*time.Hour)))
		}
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	default:
		return fmt.Sprintf("%dmo ago", int(d/(30*24*time.Hour)))
	}
}
