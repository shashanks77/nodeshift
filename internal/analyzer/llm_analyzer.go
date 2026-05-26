package analyzer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	types "github.com/your-org/nodeshift/internal"
	"github.com/your-org/nodeshift/internal/llm"
)

// LLMAnalyzeResult represents a single package upgrade suggestion from the LLM.
type LLMPackageSuggestion struct {
	Name             string `json:"name"`
	CurrentVersion   string `json:"currentVersion"`
	SuggestedVersion string `json:"suggestedVersion"`
	Reason           string `json:"reason"`
	Severity         string `json:"severity"`
}

// AnalyzeWithLLM uses the LLM to analyze package.json and determine which
// packages need upgrading for compatibility with the target Node version.
// It returns additional issues beyond what the static analyzer finds.
func AnalyzeWithLLM(client *llm.Client, repoPath string, targetVersion int) ([]types.DependencyIssue, error) {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil, nil // no package.json
	}

	system := fmt.Sprintf(`You are a Node.js upgrade expert. Analyze the given package.json and identify ALL packages that need upgrading for Node.js %d compatibility.

For each package that needs an upgrade, provide:
- name: the exact package name
- currentVersion: the current version string from package.json
- suggestedVersion: the recommended version string (e.g. "^24.0.0")
- reason: brief explanation of why the upgrade is needed
- severity: "high" (will break), "medium" (may cause issues), or "low" (recommended update)

Consider:
1. Packages with native bindings that need recompilation
2. Packages that use deprecated Node.js APIs removed in Node %d
3. Type definition packages (@types/node, @tsconfig/nodeXX) that should match target
4. Testing frameworks that need major version bumps
5. Build tools that may be incompatible
6. Any package known to have issues with Node %d
7. Deprecated/unmaintained packages that won't receive Node %d compatibility patches:
   - aws-sdk v2 (use @aws-sdk/* v3)
   - request (use axios/undici/fetch)
   - tslint (use eslint)
   - nats v1.x (use nats v2.x with JetStream)
   - moleculer 0.14.x (use 0.15+)
   - nodemon 2.x (use 3.x)

Flag packages that are deprecated or unmaintained even if not strictly a Node version issue — they will NOT receive patches for Node %d compatibility bugs.
Do NOT suggest upgrades for packages that are already on a supported version.

Respond with ONLY a JSON array of objects. No markdown, no explanation, just the JSON array.
If no packages need upgrading, respond with an empty array: []`, targetVersion, targetVersion, targetVersion, targetVersion, targetVersion)

	user := fmt.Sprintf("Here is the package.json to analyze for Node.js %d compatibility:\n\n%s", targetVersion, string(data))

	fmt.Printf("  [LLM-ANALYZE] Analyzing package.json for Node %d compatibility...\n", targetVersion)

	response, err := client.Chat(system, user)
	if err != nil {
		fmt.Printf("  [WARN] LLM analysis failed: %v\n", err)
		return nil, nil // non-fatal, fall back to static analysis
	}

	// Parse LLM response — extract JSON array
	response = strings.TrimSpace(response)
	// Strip markdown code fences if present
	if strings.HasPrefix(response, "```") {
		lines := strings.Split(response, "\n")
		// Remove first and last lines (``` markers)
		if len(lines) > 2 {
			response = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var suggestions []LLMPackageSuggestion
	if err := json.Unmarshal([]byte(response), &suggestions); err != nil {
		fmt.Printf("  [WARN] Could not parse LLM analysis response: %v\n", err)
		return nil, nil
	}

	// Convert to DependencyIssue format
	var issues []types.DependencyIssue
	for _, s := range suggestions {
		if s.Name == "" || s.SuggestedVersion == "" {
			continue // skip malformed entries
		}
		issues = append(issues, types.DependencyIssue{
			Name:             s.Name,
			CurrentVersion:   s.CurrentVersion,
			Issue:            "llm-detected",
			Severity:         s.Severity,
			Reason:           s.Reason,
			SuggestedVersion: s.SuggestedVersion,
		})
	}

	if len(issues) > 0 {
		fmt.Printf("  [LLM-ANALYZE] Found %d packages needing upgrade\n", len(issues))
	} else {
		fmt.Println("  [LLM-ANALYZE] All packages appear compatible")
	}

	return issues, nil
}
