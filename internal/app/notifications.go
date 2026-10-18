package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Keys describe material states, not dates: correcting a premiere date must not
// duplicate a delivery, while an upcoming baseline must not swallow its release.
func releaseEvent(r Release, now time.Time) string {
	switch releaseWindow(r.ReleaseDate, now) {
	case "upcoming":
		return "upcoming"
	case "current", "past":
		return "released"
	default:
		return "discovered"
	}
}
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}
func notificationMessage(r Release) string {
	unknown := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "unknown"
		}
		return s
	}
	year := "unknown"
	if r.Year > 0 {
		year = strconv.Itoa(r.Year)
	}
	age := "unknown"
	if r.AgeRating != nil {
		age = strconv.Itoa(*r.AgeRating)
	}
	header := fmt.Sprintf("%s • %s • %s\nRelease: %s\nCertification: %s | Suggested age: %s\nMatched: %s", unknown(r.Type), year, truncateRunes(unknown(r.Country), 80), unknown(r.ReleaseDate), truncateRunes(unknown(r.Certification), 80), age, truncateRunes(unknown(personNames(r.Actresses)), 240))
	header = truncateRunes(header, 650)
	synopsis := unknown(strings.Join(strings.Fields(r.Description), " "))
	return header + "\n" + truncateRunes(synopsis, 1024-len([]rune(header))-1)
}
