package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/your-org/nodeshift/internal/verify"
)

const maxAttempts = 3

// FixResult tracks what was fixed and what remains.
type FixResult struct {
	FilesFixed    []string
	AttemptsMade  int
	TscRemaining  []verify.TscError
	TestRemaining []verify.TestError
}

// FixTscErrors iterates on tsc errors using the LLM, up to maxAttempts rounds.
func FixTscErrors(client *Client, repoPath string, target int) FixResult {
	result := FixResult{}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.AttemptsMade = attempt

		ok, errors := verify.RunTsc(repoPath)
		if ok || len(errors) == 0 {
			result.TscRemaining = nil
			return result
		}

		// Group errors by file
		byFile := map[string][]verify.TscError{}
		for _, e := range errors {
			byFile[e.File] = append(byFile[e.File], e)
		}

		fixedAny := false
		for file, fileErrors := range byFile {
			fullPath := filepath.Join(repoPath, file)
			content, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}

			// Build error description
			var errDesc strings.Builder
			for _, e := range fileErrors {
				fmt.Fprintf(&errDesc, "Line %d, Col %d: %s %s\n", e.Line, e.Col, e.Code, e.Message)
			}

			prompt := fmt.Sprintf(PromptTscFix, target, file, string(content), errDesc.String())
			reply, err := client.Chat(SystemPromptTscFix, prompt)
			if err != nil {
				fmt.Printf("  [LLM] Error for %s: %v\n", file, err)
				continue
			}

			fixed := cleanCodeResponse(reply)
			if fixed == "" || fixed == string(content) {
				continue
			}

			if err := os.WriteFile(fullPath, []byte(fixed), 0644); err != nil {
				fmt.Printf("  [LLM] Failed to write %s: %v\n", file, err)
				continue
			}

			fmt.Printf("  [LLM] Fixed %s (attempt %d)\n", file, attempt)
			result.FilesFixed = append(result.FilesFixed, file)
			fixedAny = true
		}

		if !fixedAny {
			break
		}
	}

	// Final check
	_, remaining := verify.RunTsc(repoPath)
	result.TscRemaining = remaining
	return result
}

// FixTestErrors iterates on test failures using the LLM, up to maxAttempts rounds.
func FixTestErrors(client *Client, repoPath string, target int) FixResult {
	result := FixResult{}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result.AttemptsMade = attempt

		ok, errors := verify.RunTests(repoPath)
		if ok || len(errors) == 0 {
			result.TestRemaining = nil
			return result
		}

		fixedAny := false
		// Deduplicate by test file
		seen := map[string]bool{}
		for _, e := range errors {
			// Skip errors originating from node_modules (dependency issues, not test code bugs)
			if strings.Contains(e.Error, "node_modules/") {
				fmt.Printf("  [LLM] Skipping test error from dependency: %s\n", e.TestSuite)
				continue
			}
			testFile := guessTestFile(repoPath, e)
			if testFile == "" || seen[testFile] {
				continue
			}
			seen[testFile] = true

			fullPath := filepath.Join(repoPath, testFile)
			content, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}

			// Collect errors for this test file
			var errDesc strings.Builder
			for _, te := range errors {
				if guessTestFile(repoPath, te) == testFile {
					fmt.Fprintf(&errDesc, "Test: %s\nError: %s\n\n", te.TestName, te.Error)
				}
			}

			// Try to read related source files
			sourceContext := gatherSourceContext(repoPath, testFile)

			prompt := fmt.Sprintf(PromptTestFix, target, testFile, string(content), errDesc.String(), sourceContext)
			reply, err := client.Chat(SystemPromptTscFix, prompt)
			if err != nil {
				fmt.Printf("  [LLM] Error for %s: %v\n", testFile, err)
				continue
			}

			fixed := cleanCodeResponse(reply)
			if fixed == "" || fixed == string(content) {
				continue
			}

			if err := os.WriteFile(fullPath, []byte(fixed), 0644); err != nil {
				fmt.Printf("  [LLM] Failed to write %s: %v\n", testFile, err)
				continue
			}

			fmt.Printf("  [LLM] Fixed %s (attempt %d)\n", testFile, attempt)
			result.FilesFixed = append(result.FilesFixed, testFile)
			fixedAny = true
		}

		if !fixedAny {
			break
		}
	}

	_, remaining := verify.RunTests(repoPath)
	result.TestRemaining = remaining
	return result
}

// cleanCodeResponse strips markdown fences and leading/trailing whitespace.
func cleanCodeResponse(reply string) string {
	reply = strings.TrimSpace(reply)

	// Remove ```typescript ... ``` or ```ts ... ``` or ``` ... ```
	if strings.HasPrefix(reply, "```") {
		lines := strings.SplitN(reply, "\n", 2)
		if len(lines) == 2 {
			reply = lines[1]
		}
		if idx := strings.LastIndex(reply, "```"); idx >= 0 {
			reply = reply[:idx]
		}
	}

	return strings.TrimSpace(reply)
}

// guessTestFile tries to determine which test file a TestError came from.
func guessTestFile(repoPath string, te verify.TestError) string {
	if te.TestSuite != "" {
		// Jest often reports "TestSuite > TestName"
		// Try common patterns
		candidates := []string{
			te.TestSuite,
			"test/" + te.TestSuite,
			"__tests__/" + te.TestSuite,
		}
		for _, c := range candidates {
			if !strings.HasSuffix(c, ".ts") && !strings.HasSuffix(c, ".js") {
				c += ".test.ts"
			}
			full := filepath.Join(repoPath, c)
			if _, err := os.Stat(full); err == nil {
				rel, _ := filepath.Rel(repoPath, full)
				return rel
			}
		}
	}
	return ""
}

// gatherSourceContext reads the likely source file for a test file.
func gatherSourceContext(repoPath, testFile string) string {
	// test/helper/Utils.test.ts → src/helper/Utils.ts
	srcFile := testFile
	srcFile = strings.Replace(srcFile, "test/", "src/", 1)
	srcFile = strings.Replace(srcFile, "__tests__/", "src/", 1)
	srcFile = strings.Replace(srcFile, ".test.ts", ".ts", 1)
	srcFile = strings.Replace(srcFile, ".spec.ts", ".ts", 1)

	fullPath := filepath.Join(repoPath, srcFile)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "(source file not found)"
	}

	// Truncate to ~200 lines to stay within token limits
	lines := strings.Split(string(content), "\n")
	if len(lines) > 200 {
		lines = lines[:200]
		lines = append(lines, "// ... truncated")
	}

	return fmt.Sprintf("File: %s\n```\n%s\n```", srcFile, strings.Join(lines, "\n"))
}

// FixVulnerabilities uses the LLM to upgrade vulnerable dependency versions in package.json.
// Only sends direct dependencies with critical/high severity to keep the prompt small and focused.
func FixVulnerabilities(client *Client, repoPath string, vulns []verify.Vulnerability) FixResult {
	result := FixResult{}

	// Filter to only direct deps with critical/high severity (actionable by version bump)
	var actionable []verify.Vulnerability
	for _, v := range vulns {
		if v.IsDirect && (v.Severity == "critical" || v.Severity == "high") {
			actionable = append(actionable, v)
		}
	}
	if len(actionable) == 0 {
		fmt.Println("  [LLM] No direct critical/high vulnerabilities to fix")
		return result
	}

	pkgPath := filepath.Join(repoPath, "package.json")
	content, err := os.ReadFile(pkgPath)
	if err != nil {
		fmt.Printf("  [LLM] Cannot read package.json: %v\n", err)
		return result
	}

	// Build vulnerability description (only actionable ones)
	var vulnDesc strings.Builder
	for _, v := range actionable {
		fmt.Fprintf(&vulnDesc, "- %s [%s]: %s (direct dependency)\n", v.Name, v.Severity, v.Title)
		if v.URL != "" {
			fmt.Fprintf(&vulnDesc, "  Advisory: %s\n", v.URL)
		}
	}
	fmt.Printf("  [LLM] Sending %d direct critical/high vulnerabilities to LLM\n", len(actionable))

	prompt := fmt.Sprintf(PromptVulnFix, string(content), vulnDesc.String())

	reply, err := client.Chat(SystemPromptVulnFix, prompt)
	if err != nil {
		fmt.Printf("  [LLM] Error: %v\n", err)
		return result
	}

	fixed := cleanCodeResponse(reply)
	if fixed == "" || fixed == string(content) {
		return result
	}

	// Validate it's still valid JSON
	var js map[string]interface{}
	if err := json.Unmarshal([]byte(fixed), &js); err != nil {
		fmt.Printf("  [LLM] Invalid JSON returned, skipping\n")
		return result
	}

	if err := os.WriteFile(pkgPath, []byte(fixed), 0644); err != nil {
		fmt.Printf("  [LLM] Failed to write package.json: %v\n", err)
		return result
	}

	// Run npm install to update lockfile
	npmOk, npmErr := verify.RunNpmInstall(repoPath)
	if !npmOk {
		fmt.Printf("  [LLM] npm install failed after package.json update: %s\n", npmErr)
		// Revert
		os.WriteFile(pkgPath, content, 0644)
		verify.RunNpmInstall(repoPath)
		return result
	}

	result.FilesFixed = append(result.FilesFixed, "package.json", "package-lock.json")
	result.AttemptsMade = 1
	return result
}
