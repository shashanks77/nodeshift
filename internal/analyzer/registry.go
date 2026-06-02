package analyzer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NpmPackageInfo holds the latest version info from the npm registry.
type NpmPackageInfo struct {
	Name   string
	Latest string
	Error  error
}

// FetchLatestVersions queries the npm registry for the latest stable version of each package.
// Uses concurrent requests (max 5) for performance.
func FetchLatestVersions(deps map[string]string) map[string]string {
	result := make(map[string]string)
	mu := sync.Mutex{}
	sem := make(chan struct{}, 5) // concurrency limit
	wg := sync.WaitGroup{}

	client := &http.Client{Timeout: 10 * time.Second}

	for name := range deps {
		wg.Add(1)
		go func(pkgName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			latest, err := fetchLatestFromRegistry(client, pkgName)
			if err != nil {
				return
			}

			mu.Lock()
			result[pkgName] = latest
			mu.Unlock()
		}(name)
	}

	wg.Wait()
	return result
}

func fetchLatestFromRegistry(client *http.Client, pkgName string) (string, error) {
	// Handle scoped packages: @scope/pkg → %40scope%2Fpkg
	url := "https://registry.npmjs.org/" + pkgName + "/latest"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("npm registry returned %d for %s", resp.StatusCode, pkgName)
	}

	var meta struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", err
	}

	return meta.Version, nil
}

// CompareVersions returns:
//
//	"major" if latest is a major bump over current
//	"minor" if latest is a minor bump
//	"patch" if latest is a patch bump
//	"" if current >= latest or versions can't be parsed
func CompareVersions(current, latest string) string {
	curMajor, curMinor, curPatch := parseSemanticVersion(current)
	latMajor, latMinor, latPatch := parseSemanticVersion(latest)

	if curMajor < 0 || latMajor < 0 {
		return ""
	}

	if latMajor > curMajor {
		return "major"
	}
	if latMinor > curMinor && latMajor == curMajor {
		return "minor"
	}
	if latPatch > curPatch && latMajor == curMajor && latMinor == curMinor {
		return "patch"
	}
	return ""
}

var reSemanticVersion = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parseSemanticVersion extracts major.minor.patch from a version string like "^1.2.3" or "1.2.3".
func parseSemanticVersion(ver string) (int, int, int) {
	// Strip leading ^, ~, >=, etc.
	ver = strings.TrimLeft(ver, "^~>=<! ")

	m := reSemanticVersion.FindStringSubmatch(ver)
	if len(m) < 4 {
		// Try just major
		rm := reVersion.FindStringSubmatch(ver)
		if len(rm) >= 2 {
			major, _ := strconv.Atoi(rm[1])
			return major, 0, 0
		}
		return -1, -1, -1
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return major, minor, patch
}
