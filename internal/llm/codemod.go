package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	types "github.com/your-org/nodeshift/internal"
)

// FixDeprecatedAPIs uses the LLM to migrate source files that use deprecated/changed APIs
// from upgraded dependencies. Runs after deterministic codemods and package.json transforms.
func FixDeprecatedAPIs(client *Client, repoPath string, target int, issues []types.DependencyIssue) FixResult {
	result := FixResult{}

	// Only process issues that involve actual package upgrades/replacements
	var upgradedPkgs []types.DependencyIssue
	for _, issue := range issues {
		if issue.SuggestedVersion != "" && (issue.Issue == "outdated" || issue.Issue == "incompatible" || issue.Issue == "eol") {
			upgradedPkgs = append(upgradedPkgs, issue)
		}
	}

	if len(upgradedPkgs) == 0 {
		return result
	}

	// Build upgrade description for the prompt
	var upgradeDesc strings.Builder
	for _, pkg := range upgradedPkgs {
		fmt.Fprintf(&upgradeDesc, "- %s: %s → %s (%s)\n", pkg.Name, pkg.CurrentVersion, pkg.SuggestedVersion, pkg.Reason)
	}

	// Build list of package names to search for in imports
	pkgNames := make(map[string]bool)
	for _, pkg := range upgradedPkgs {
		pkgNames[pkg.Name] = true
	}

	// Find source files that import any of the upgraded packages
	affectedFiles := findAffectedFiles(repoPath, pkgNames)
	if len(affectedFiles) == 0 {
		fmt.Println("  [LLM-CODEMOD] No source files import the upgraded packages")
		return result
	}

	fmt.Printf("  [LLM-CODEMOD] Found %d file(s) importing upgraded packages\n", len(affectedFiles))

	for _, file := range affectedFiles {
		relPath, _ := filepath.Rel(repoPath, file)
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		// Skip files that are too large (>500 lines) - LLM context limits
		lines := strings.Count(string(content), "\n")
		if lines > 500 {
			fmt.Printf("  [LLM-CODEMOD] Skipping %s (too large: %d lines)\n", relPath, lines)
			continue
		}

		prompt := fmt.Sprintf(PromptCodemod, target, upgradeDesc.String(), relPath, string(content))
		reply, err := client.Chat(SystemPromptCodemod, prompt)
		if err != nil {
			fmt.Printf("  [LLM-CODEMOD] Error for %s: %v\n", relPath, err)
			continue
		}

		fixed := cleanCodeResponse(reply)
		if fixed == "" || fixed == string(content) {
			continue
		}

		if err := os.WriteFile(file, []byte(fixed), 0644); err != nil {
			fmt.Printf("  [LLM-CODEMOD] Failed to write %s: %v\n", relPath, err)
			continue
		}

		fmt.Printf("  [LLM-CODEMOD] Migrated %s\n", relPath)
		result.FilesFixed = append(result.FilesFixed, relPath)
	}

	result.AttemptsMade = 1
	return result
}

// findAffectedFiles walks the repo's source directories and returns files that
// import any of the given package names.
func findAffectedFiles(repoPath string, pkgNames map[string]bool) []string {
	var affected []string

	srcDirs := []string{"src", "lib", "apps", "libs"}
	for _, dir := range srcDirs {
		dirPath := filepath.Join(repoPath, dir)
		if _, err := os.Stat(dirPath); os.IsNotExist(err) {
			continue
		}

		filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".js") {
				return nil
			}
			// Skip test files - they'll be handled by test fix phase
			if strings.Contains(path, ".spec.") || strings.Contains(path, ".test.") || strings.Contains(path, "__tests__") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			content := string(data)

			for name := range pkgNames {
				// Check for import/require of this package
				if strings.Contains(content, "'"+name+"'") ||
					strings.Contains(content, "\""+name+"\"") ||
					strings.Contains(content, "'"+name+"/") ||
					strings.Contains(content, "\""+name+"/") {
					affected = append(affected, path)
					break
				}
			}
			return nil
		})
	}

	return affected
}
