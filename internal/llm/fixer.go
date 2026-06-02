package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	types "github.com/your-org/nodeshift/internal"
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

			// Gather type definitions from node_modules for packages referenced in errors
			typeContext := gatherTypeContext(repoPath, fileErrors, string(content))

			prompt := fmt.Sprintf(PromptTscFix, target, file, string(content), errDesc.String(), typeContext)
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
// Handles all vulnerabilities: direct deps first (all severities), then transitive deps
// by identifying and bumping the root parent dependency.
func FixVulnerabilities(client *Client, repoPath string, vulns []verify.Vulnerability) FixResult {
	result := FixResult{}

	if len(vulns) == 0 {
		return result
	}

	pkgPath := filepath.Join(repoPath, "package.json")
	content, err := os.ReadFile(pkgPath)
	if err != nil {
		fmt.Printf("  [LLM] Cannot read package.json: %v\n", err)
		return result
	}

	// Separate direct and transitive vulnerabilities
	var directVulns []verify.Vulnerability
	var transitiveVulns []verify.Vulnerability
	for _, v := range vulns {
		if v.IsDirect {
			directVulns = append(directVulns, v)
		} else {
			transitiveVulns = append(transitiveVulns, v)
		}
	}

	// Build comprehensive vulnerability description
	var vulnDesc strings.Builder
	if len(directVulns) > 0 {
		fmt.Fprintf(&vulnDesc, "DIRECT DEPENDENCIES (bump these directly):\n")
		for _, v := range directVulns {
			fmt.Fprintf(&vulnDesc, "- %s [%s]: %s\n", v.Name, v.Severity, v.Title)
			if v.URL != "" {
				fmt.Fprintf(&vulnDesc, "  Advisory: %s\n", v.URL)
			}
		}
	}
	if len(transitiveVulns) > 0 {
		fmt.Fprintf(&vulnDesc, "\nTRANSITIVE DEPENDENCIES (bump the parent package that pulls these in):\n")
		for _, v := range transitiveVulns {
			fmt.Fprintf(&vulnDesc, "- %s [%s]: %s\n", v.Name, v.Severity, v.Title)
			if v.URL != "" {
				fmt.Fprintf(&vulnDesc, "  Advisory: %s\n", v.URL)
			}
		}
	}

	totalCount := len(directVulns) + len(transitiveVulns)
	fmt.Printf("  [LLM] Sending %d vulnerabilities to LLM (%d direct, %d transitive)\n",
		totalCount, len(directVulns), len(transitiveVulns))

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

// DetectMajorBumps compares the current package.json deps with a previous snapshot
// and returns synthetic DependencyIssues for packages that were bumped across majors.
// This is used after npm audit fix --force to identify packages needing codemod.
func DetectMajorBumps(repoPath string, beforePkgJSON []byte) []types.DependencyIssue {
	var issues []types.DependencyIssue

	var before struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(beforePkgJSON, &before); err != nil {
		return issues
	}

	pkgPath := filepath.Join(repoPath, "package.json")
	afterData, err := os.ReadFile(pkgPath)
	if err != nil {
		return issues
	}

	var after struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(afterData, &after); err != nil {
		return issues
	}

	// Merge before and after
	allBefore := mergeMaps(before.Dependencies, before.DevDependencies)
	allAfter := mergeMaps(after.Dependencies, after.DevDependencies)

	for name, afterVer := range allAfter {
		beforeVer, existed := allBefore[name]
		if !existed {
			continue
		}
		bump := compareVersions(beforeVer, afterVer)
		if bump == "major" {
			issues = append(issues, types.DependencyIssue{
				Name:             name,
				CurrentVersion:   beforeVer,
				Issue:            "outdated",
				Severity:         "high",
				Reason:           fmt.Sprintf("%s was bumped from %s to %s by audit fix (major version change — may have breaking API changes).", name, beforeVer, afterVer),
				SuggestedVersion: afterVer,
			})
		}
	}

	return issues
}

func mergeMaps(maps ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}

var reSemanticVer = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)
var reVersionNum = regexp.MustCompile(`(\d+)`)

// compareVersions returns "major", "minor", "patch", or "" if current >= latest.
func compareVersions(current, latest string) string {
	curMajor, curMinor, curPatch := parseSemVer(current)
	latMajor, latMinor, latPatch := parseSemVer(latest)

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

func parseSemVer(ver string) (int, int, int) {
	ver = strings.TrimLeft(ver, "^~>=<! ")
	m := reSemanticVer.FindStringSubmatch(ver)
	if len(m) < 4 {
		rm := reVersionNum.FindStringSubmatch(ver)
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

// gatherTypeContext extracts relevant .d.ts type definitions from node_modules
// for packages referenced in tsc errors. This gives the LLM context about the
// actual installed package API so it can fix type mismatches from major upgrades.
func gatherTypeContext(repoPath string, errors []verify.TscError, fileContent string) string {
	// Extract package names from error messages and the file's imports
	packages := extractRelevantPackages(errors, fileContent)
	if len(packages) == 0 {
		return ""
	}

	var ctx strings.Builder
	ctx.WriteString("\nRelevant type definitions from installed packages:\n")

	totalLines := 0
	const maxTotalLines = 500 // Cap total type context to stay within token limits

	for _, pkg := range packages {
		if totalLines >= maxTotalLines {
			break
		}

		dtsContent := readPackageTypes(repoPath, pkg)
		if dtsContent == "" {
			continue
		}

		// Truncate per-package
		lines := strings.Split(dtsContent, "\n")
		remaining := maxTotalLines - totalLines
		if len(lines) > remaining {
			lines = lines[:remaining]
			lines = append(lines, "// ... truncated")
		}
		totalLines += len(lines)

		fmt.Fprintf(&ctx, "\nPackage: %s\n```typescript\n%s\n```\n", pkg, strings.Join(lines, "\n"))

		// For deep subpaths, also include the root package's top-level index.d.ts
		// so the LLM can see the full namespace structure (e.g., export { types })
		parts := strings.Split(pkg, "/")
		var rootPkg string
		if strings.HasPrefix(pkg, "@") && len(parts) >= 3 {
			rootPkg = parts[0] + "/" + parts[1]
		} else if !strings.HasPrefix(pkg, "@") && len(parts) >= 2 {
			rootPkg = parts[0]
		}
		if rootPkg != "" && rootPkg != pkg && totalLines < maxTotalLines {
			rootDts := readPackageTypes(repoPath, rootPkg)
			if rootDts != "" {
				rLines := strings.Split(rootDts, "\n")
				remaining = maxTotalLines - totalLines
				if len(rLines) > remaining {
					rLines = rLines[:remaining]
					rLines = append(rLines, "// ... truncated")
				}
				totalLines += len(rLines)
				fmt.Fprintf(&ctx, "\nPackage (root): %s\n```typescript\n%s\n```\n", rootPkg, strings.Join(rLines, "\n"))
			}

			// Also extract specific type/interface definitions referenced in errors
			typeNames := extractTypeNamesFromErrors(errors)
			if len(typeNames) > 0 {
				typeDefs := findTypeDefinitions(repoPath, rootPkg, typeNames)
				if typeDefs != "" {
					tLines := strings.Split(typeDefs, "\n")
					remaining = maxTotalLines - totalLines
					if len(tLines) > remaining {
						tLines = tLines[:remaining]
						tLines = append(tLines, "// ... truncated")
					}
					totalLines += len(tLines)
					fmt.Fprintf(&ctx, "\nType definitions for referenced types:\n```typescript\n%s\n```\n", strings.Join(tLines, "\n"))
				}
			}
		}
	}

	if totalLines == 0 {
		return ""
	}
	return ctx.String()
}

// extractRelevantPackages identifies which packages are relevant to the tsc errors
// by examining error messages (for module paths) and the source file imports.
// Returns package paths that may include subpaths (e.g., "@pulumi/awsx/ecs").
func extractRelevantPackages(errors []verify.TscError, fileContent string) []string {
	pkgSet := map[string]bool{}

	// Pattern: node_modules/@scope/pkg/subpath or node_modules/pkg/subpath
	reNodeModulesDeep := regexp.MustCompile(`node_modules/((?:@[^/]+/[^/]+(?:/[^/"']+)*)|(?:[^/@][^/"']+(?:/[^/"']+)*))`)
	// Pattern: from '@scope/pkg' or from 'pkg' or from '@scope/pkg/sub'
	reImport := regexp.MustCompile(`(?:from|import)\s+['"]((@[^/'"]+/[^'"]+)|([^./'"@][^'"]*))['"]`)
	// Pattern: type names mentioned in errors
	reType := regexp.MustCompile(`type '([^']+)'`)

	// Scan error messages for module references (including deep paths)
	for _, e := range errors {
		matches := reNodeModulesDeep.FindAllStringSubmatch(e.Message, -1)
		for _, m := range matches {
			// Add both the deep path and the root package
			deepPath := m[1]
			pkgSet[deepPath] = true
			// Also add root package (first 1 or 2 segments for scoped)
			parts := strings.Split(deepPath, "/")
			if strings.HasPrefix(deepPath, "@") && len(parts) >= 2 {
				pkgSet[parts[0]+"/"+parts[1]] = true
			} else if len(parts) >= 1 {
				pkgSet[parts[0]] = true
			}
		}
	}

	// If no packages found from errors, scan the file imports for packages
	// that are likely involved (match import names against error messages)
	if len(pkgSet) == 0 {
		importMatches := reImport.FindAllStringSubmatch(fileContent, -1)
		typeNames := map[string]bool{}
		for _, e := range errors {
			for _, m := range reType.FindAllStringSubmatch(e.Message, -1) {
				typeNames[m[1]] = true
			}
		}
		for _, im := range importMatches {
			pkg := im[1]
			for _, e := range errors {
				if strings.Contains(e.Message, pkg) {
					pkgSet[pkg] = true
				}
			}
			for tn := range typeNames {
				if strings.Contains(tn, pkg) {
					pkgSet[pkg] = true
				}
			}
		}
	}

	var result []string
	for pkg := range pkgSet {
		result = append(result, pkg)
	}
	return result
}

// readPackageTypes reads the relevant .d.ts index file for a package from node_modules.
// Handles both root packages and deep subpaths (e.g., "@pulumi/awsx/ecs").
func readPackageTypes(repoPath, pkg string) string {
	nodeModules := filepath.Join(repoPath, "node_modules", pkg)

	// For deep paths like "@pulumi/awsx/ecs/index", try the path directly first
	directDts := nodeModules + ".d.ts"
	if content, err := os.ReadFile(directDts); err == nil {
		return filterRelevantDeclarations(string(content))
	}
	// Try as directory with index.d.ts — also scan sibling .d.ts files for exported interfaces
	indexDts := filepath.Join(nodeModules, "index.d.ts")
	if content, err := os.ReadFile(indexDts); err == nil {
		result := filterRelevantDeclarations(string(content))
		// If the index is mostly re-exports, also read sibling .d.ts files
		// to provide the actual interface definitions
		siblings := readSiblingDtsExports(nodeModules)
		if siblings != "" {
			result += "\n" + siblings
		}
		return result
	}

	// Check package.json "types" or "typings" field
	pkgJSON := filepath.Join(nodeModules, "package.json")
	if data, err := os.ReadFile(pkgJSON); err == nil {
		var pj struct {
			Types   string `json:"types"`
			Typings string `json:"typings"`
		}
		if json.Unmarshal(data, &pj) == nil {
			typesEntry := pj.Types
			if typesEntry == "" {
				typesEntry = pj.Typings
			}
			if typesEntry != "" {
				typesPath := filepath.Join(nodeModules, typesEntry)
				if content, err := os.ReadFile(typesPath); err == nil {
					return filterRelevantDeclarations(string(content))
				}
			}
		}
	}

	// Fallback: other common locations
	candidates := []string{
		filepath.Join(nodeModules, "dist", "index.d.ts"),
		filepath.Join(nodeModules, "lib", "index.d.ts"),
	}

	// Also check @types package for the root package name
	parts := strings.Split(pkg, "/")
	var rootPkg string
	if strings.HasPrefix(pkg, "@") && len(parts) >= 2 {
		rootPkg = parts[0] + "/" + parts[1]
	} else {
		rootPkg = parts[0]
	}
	typesPackage := strings.Replace(rootPkg, "/", "__", -1)
	candidates = append(candidates, filepath.Join(repoPath, "node_modules", "@types", typesPackage, "index.d.ts"))

	for _, candidate := range candidates {
		if content, err := os.ReadFile(candidate); err == nil {
			return filterRelevantDeclarations(string(content))
		}
	}

	// For deep subpaths, also try reading the types/input.d.ts (common for Pulumi-like packages)
	if rootPkg != pkg {
		typesInput := filepath.Join(repoPath, "node_modules", rootPkg, "types", "input.d.ts")
		if content, err := os.ReadFile(typesInput); err == nil {
			// Only return relevant sections — search for subpath-related types
			subpath := strings.TrimPrefix(pkg, rootPkg+"/")
			return filterBySubpath(string(content), subpath)
		}
	}

	return ""
}

// filterBySubpath extracts only the namespace/section relevant to a subpath from a large .d.ts file.
func filterBySubpath(content, subpath string) string {
	lines := strings.Split(content, "\n")
	var result []string
	depth := 0
	capturing := false

	// Look for namespace matching the subpath (e.g., "ecs" for "@pulumi/awsx/ecs")
	target := subpath
	if idx := strings.LastIndex(subpath, "/"); idx >= 0 {
		target = subpath[idx+1:]
	}
	namespacePattern := "namespace " + target

	for _, line := range lines {
		if !capturing {
			if strings.Contains(line, namespacePattern) {
				capturing = true
				depth = 0
			}
		}
		if capturing {
			result = append(result, line)
			depth += strings.Count(line, "{") - strings.Count(line, "}")
			if depth <= 0 && len(result) > 1 {
				break
			}
			// Safety cap
			if len(result) > 200 {
				result = append(result, "// ... truncated")
				break
			}
		}
	}

	if len(result) == 0 {
		// Fallback: return the full content filtered
		return filterRelevantDeclarations(content)
	}
	return filterRelevantDeclarations(strings.Join(result, "\n"))
}

// filterRelevantDeclarations filters .d.ts content to keep only exported interfaces,
// types, classes, and function signatures — strips comments and implementation details.
func filterRelevantDeclarations(content string) string {
	lines := strings.Split(content, "\n")
	var filtered []string
	inBlockComment := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip block comments
		if strings.Contains(trimmed, "/*") {
			inBlockComment = true
		}
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}

		// Skip single-line comments
		if strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Skip empty lines
		if trimmed == "" {
			continue
		}

		filtered = append(filtered, line)
	}

	return strings.Join(filtered, "\n")
}

// extractTypeNamesFromErrors extracts type/interface names mentioned in tsc error messages.
func extractTypeNamesFromErrors(errors []verify.TscError) []string {
	// Match PascalCase type names (with optional Args/Type suffix) anywhere in quoted strings
	reTypeName := regexp.MustCompile(`'([A-Z][A-Za-z]{2,})'|type '([A-Z][A-Za-z]{2,})[^']*'`)
	// Also match type names that appear before | or in "to type 'Name | ..."
	reTypeInUnion := regexp.MustCompile(`type '([A-Z][A-Za-z]{2,})`)
	seen := map[string]bool{}
	var names []string

	for _, e := range errors {
		// Primary: exact quoted type names
		matches := reTypeName.FindAllStringSubmatch(e.Message, -1)
		for _, m := range matches {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if name != "" && !seen[name] && len(name) > 4 {
				seen[name] = true
				names = append(names, name)
			}
		}
		// Also catch types in union positions like 'DefaultRoleWithPolicyArgs | undefined'
		unionMatches := reTypeInUnion.FindAllStringSubmatch(e.Message, -1)
		for _, m := range unionMatches {
			if m[1] != "" && !seen[m[1]] && len(m[1]) > 4 {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	return names
}

// findTypeDefinitions searches for interface/type definitions by name in a package's type files.
func findTypeDefinitions(repoPath, rootPkg string, typeNames []string) string {
	// Search in common type definition files
	candidates := []string{
		filepath.Join(repoPath, "node_modules", rootPkg, "types", "input.d.ts"),
		filepath.Join(repoPath, "node_modules", rootPkg, "types", "output.d.ts"),
		filepath.Join(repoPath, "node_modules", rootPkg, "types", "index.d.ts"),
	}

	var result strings.Builder

	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		content := string(data)
		lines := strings.Split(content, "\n")

		for _, typeName := range typeNames {
			// Find the interface/type definition
			pattern := "interface " + typeName
			for i, line := range lines {
				if strings.Contains(line, pattern) {
					// Determine namespace path by scanning backwards
					nsPath := findNamespacePath(lines, i)
					if nsPath != "" {
						fmt.Fprintf(&result, "// Access path: %s.types.input.%s.%s\n", rootPkg, nsPath, typeName)
					} else {
						fmt.Fprintf(&result, "// Access path: %s.types.input.%s\n", rootPkg, typeName)
					}

					// Capture the full interface definition
					depth := 0
					started := false
					for j := i; j < len(lines) && j < i+50; j++ {
						result.WriteString(lines[j])
						result.WriteString("\n")
						depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
						if strings.Contains(lines[j], "{") {
							started = true
						}
						if started && depth <= 0 {
							break
						}
					}
					result.WriteString("\n")
					break
				}
			}
		}
	}

	return result.String()
}

// findNamespacePath scans backwards from a line to find the enclosing namespace(s).
func findNamespacePath(lines []string, fromLine int) string {
	var namespaces []string
	depth := 0

	// Count the nesting depth at the target line
	for i := 0; i < fromLine; i++ {
		depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
	}

	// Walk backwards to find namespace declarations at each nesting level
	targetDepth := depth
	for d := targetDepth; d > 0; d-- {
		currentDepth := 0
		for i := 0; i < fromLine; i++ {
			if currentDepth == d-1 && strings.Contains(lines[i], "namespace ") {
				// Extract namespace name
				parts := strings.Fields(lines[i])
				for j, p := range parts {
					if p == "namespace" && j+1 < len(parts) {
						ns := strings.TrimRight(parts[j+1], " {")
						namespaces = append([]string{ns}, namespaces...)
						break
					}
				}
				break
			}
			currentDepth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
		}
	}

	return strings.Join(namespaces, ".")
}

// readSiblingDtsExports reads sibling .d.ts files in a directory and extracts
// exported interfaces and type definitions. This is useful when index.d.ts is
// just a barrel file with re-exports and the actual types are in siblings.
func readSiblingDtsExports(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var result strings.Builder
	totalLines := 0
	const maxLines = 200

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".d.ts") || entry.Name() == "index.d.ts" {
			continue
		}
		if totalLines >= maxLines {
			break
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		// Extract only exported interface/type/class definitions
		lines := strings.Split(string(data), "\n")
		capturing := false
		depth := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)

			// Start capturing at exported interface/type/class
			if !capturing && (strings.Contains(trimmed, "export interface ") ||
				strings.Contains(trimmed, "export type ") ||
				strings.Contains(trimmed, "export declare") ||
				strings.Contains(trimmed, "export class ")) {
				capturing = true
				depth = 0
			}

			if capturing {
				result.WriteString(line)
				result.WriteString("\n")
				totalLines++
				depth += strings.Count(line, "{") - strings.Count(line, "}")
				if depth <= 0 && totalLines > 1 {
					capturing = false
					result.WriteString("\n")
				}
				if totalLines >= maxLines {
					result.WriteString("// ... truncated\n")
					break
				}
			}
		}
	}

	return result.String()
}
