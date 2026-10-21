package app

import (
	"testing"
	"time"
)

func TestCreditRetryPrioritizesUnattemptedAndDefersMissing(t *testing.T) {
	now := time.Now().UTC()
	c := tmdbCreditCache{Credits: []tmdbCredit{{ID: "1", Kind: "movie"}, {ID: "2", Kind: "movie"}}}
	c.Credits[0].Attempts = 1
	c.Credits[0].NextRetry = now.Add(time.Hour)
	selected := c.selectCredits(now, 10)
	if len(selected["movie"]) != 1 || selected["movie"][0] != 1 {
		t.Fatalf("deferred title consumed budget: %+v", selected)
	}
	c.Credits[0].NextRetry = now.Add(-time.Hour)
	c.Next = 0
	selected = c.selectCredits(now, 1)
	if len(selected["movie"]) != 1 || selected["movie"][0] != 1 {
		t.Fatalf("retry starved new work: %+v", selected)
	}
	c.Credits[0].deferRetry(now)
	if c.Credits[0].Resolved || c.Credits[0].Attempts != 2 || !c.Credits[0].NextRetry.After(now) {
		t.Fatalf("missing title incorrectly completed: %+v", c.Credits[0])
	}
}
