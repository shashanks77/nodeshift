package analyzer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

	target := strconv.Itoa(targetVersion)
	system := strings.ReplaceAll(llm.SystemPromptAnalyze, "{{TARGET}}", target)
	user := strings.ReplaceAll(llm.PromptAnalyze, "{{TARGET}}", target)
	user = strings.ReplaceAll(user, "{{PACKAGE_JSON}}", string(data))

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
