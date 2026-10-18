package app

import (
	"encoding/json"
	"testing"
)

func TestSiloSettingsRedaction(t *testing.T) {
	s := defaultSettings()
	s.Silo = SiloSettings{URL: "http://silo.example", APIKey: "synthetic-secret", Enabled: true}
	b, _ := json.Marshal(s.public())
	if containsBytes(b, "synthetic-secret") || !s.public().Silo.Configured {
		t.Fatalf("unsafe public settings: %s", b)
	}
}
