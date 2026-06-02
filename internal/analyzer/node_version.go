package analyzer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type nodeRelease struct {
	Version string `json:"version"`
	LTS     interface{} `json:"lts"` // false or string like "Jod"
}

// FetchLatestStableNode fetches the latest LTS Node.js version from nodejs.org.
// Returns the major version number (e.g., 24).
func FetchLatestStableNode() (int, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://nodejs.org/dist/index.json")
	if err != nil {
		return 0, fmt.Errorf("fetch node releases: %w", err)
	}
	defer resp.Body.Close()

	var releases []nodeRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return 0, fmt.Errorf("decode node releases: %w", err)
	}

	// Find the latest LTS version
	for _, r := range releases {
		if r.LTS != nil && r.LTS != false {
			// LTS is a string (codename) when active
			if _, ok := r.LTS.(string); ok {
				major := extractMajor(r.Version)
				if major > 0 {
					return major, nil
				}
			}
		}
	}

	// Fallback: return the latest current version
	if len(releases) > 0 {
		major := extractMajor(releases[0].Version)
		if major > 0 {
			return major, nil
		}
	}

	return 0, fmt.Errorf("could not determine latest stable Node version")
}
