package cmd

import (
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/session"
)

func setupCostsTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestDeriveSessionName(t *testing.T) {
	setupCostsTestRegistry(t)
	tests := []struct {
		name     string
		envVars  map[string]string
		expected string
	}{
		{
			name: "polecat session",
			envVars: map[string]string{
				"GT_ROLE":    "polecat",
				"GT_RIG":     "gastown",
				"GT_POLECAT": "toast",
			},
			expected: "gt-toast",
		},
		{
			name: "crew session",
			envVars: map[string]string{
				"GT_ROLE": "crew",
				"GT_RIG":  "gastown",
				"GT_CREW": "max",
			},
			expected: "gt-crew-max",
		},
		{
			name: "witness session",
			envVars: map[string]string{
				"GT_ROLE": "witness",
				"GT_RIG":  "gastown",
			},
			expected: "gt-witness",
		},
		{
			name: "refinery session",
			envVars: map[string]string{
				"GT_ROLE": "refinery",
				"GT_RIG":  "gastown",
			},
			expected: "gt-refinery",
		},
		{
			name: "mayor session",
			envVars: map[string]string{
				"GT_ROLE": "mayor",
				"GT_TOWN": "ai",
			},
			expected: "hq-mayor",
		},
		{
			name: "deacon session",
			envVars: map[string]string{
				"GT_ROLE": "deacon",
				"GT_TOWN": "ai",
			},
			expected: "hq-deacon",
		},
		{
			name: "mayor session without GT_TOWN",
			envVars: map[string]string{
				"GT_ROLE": "mayor",
			},
			expected: "hq-mayor",
		},
		{
			name: "deacon session without GT_TOWN",
			envVars: map[string]string{
				"GT_ROLE": "deacon",
			},
			expected: "hq-deacon",
		},
		{
			name:     "no env vars",
			envVars:  map[string]string{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and clear relevant env vars
			saved := make(map[string]string)
			envKeys := []string{"GT_ROLE", "GT_RIG", "GT_POLECAT", "GT_CREW", "GT_TOWN"}
			for _, key := range envKeys {
				saved[key] = os.Getenv(key)
				os.Unsetenv(key)
			}
			defer func() {
				// Restore env vars
				for key, val := range saved {
					if val != "" {
						os.Setenv(key, val)
					}
				}
			}()

			// Set test env vars
			for key, val := range tt.envVars {
				os.Setenv(key, val)
			}

			result := deriveSessionName()
			if result != tt.expected {
				t.Errorf("deriveSessionName() = %q, want %q", result, tt.expected)
			}
		})
	}
}
