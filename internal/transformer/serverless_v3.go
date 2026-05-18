package transformer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ServerlessV3MaxRuntime is the maximum Node.js runtime supported by Serverless Framework v3.
const ServerlessV3MaxRuntime = 20

// TransformServerlessV3Compat applies Serverless Framework v3 compatibility fixes
// to serverless.yml and package.json. It handles:
//   - Flattening service object notation to flat string
//   - Capping runtime to nodejs20.x (max supported by SLS v3)
//   - Removing serverless-pseudo-parameters plugin
//   - Moving resourcePolicy under provider.apiGateway
//   - Upgrading serverless-step-functions to v3
func TransformServerlessV3Compat(repoPath string, targetNodeVersion int) ([]string, error) {
	slsFile := findServerlessFile(repoPath)
	if slsFile == "" {
		return nil, nil // not a serverless project
	}

	// Check if this project uses Serverless Framework v3
	slsVersion := detectServerlessVersion(repoPath)
	if slsVersion < 3 {
		return nil, nil // only apply to SLS v3+
	}

	var changed []string

	// Apply serverless.yml fixes
	slsChanged, err := applyServerlessYmlFixes(repoPath, slsFile, targetNodeVersion)
	if err != nil {
		return changed, fmt.Errorf("serverless v3 compat: %w", err)
	}
	if slsChanged {
		rel, _ := filepath.Rel(repoPath, slsFile)
		changed = append(changed, rel)
	}

	// Apply package.json plugin upgrades
	pkgChanged, err := upgradeServerlessPlugins(repoPath)
	if err != nil {
		return changed, fmt.Errorf("serverless v3 plugin upgrades: %w", err)
	}
	if pkgChanged {
		changed = append(changed, "package.json")
	}

	return changed, nil
}

// findServerlessFile returns the full path to serverless.yml/yaml or "".
func findServerlessFile(repoPath string) string {
	for _, name := range []string{"serverless.yml", "serverless.yaml"} {
		p := filepath.Join(repoPath, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// detectServerlessVersion reads package.json to determine the Serverless Framework major version.
// Returns 0 if not found, 2 for v2, 3 for v3, etc.
func detectServerlessVersion(repoPath string) int {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return 0
	}

	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return 0
	}

	// Check devDependencies and dependencies for "serverless"
	for _, section := range []string{"devDependencies", "dependencies"} {
		deps, ok := pkg[section].(map[string]interface{})
		if !ok {
			continue
		}
		ver, ok := deps["serverless"].(string)
		if !ok {
			continue
		}
		// Parse major from semver: "^3.40.0" → 3, "~3.0.0" → 3, "3.x" → 3
		ver = strings.TrimLeft(ver, "^~>=<")
		if len(ver) > 0 && ver[0] >= '0' && ver[0] <= '9' {
			major := 0
			for _, ch := range ver {
				if ch >= '0' && ch <= '9' {
					major = major*10 + int(ch-'0')
				} else {
					break
				}
			}
			return major
		}
	}

	// Also check if serverless is available as a local binary (installed via npm)
	slsBin := filepath.Join(repoPath, "node_modules", ".bin", "serverless")
	if _, err := os.Stat(slsBin); err == nil {
		// If binary exists but no version in package.json, assume v3 (safe default)
		return 3
	}

	// If serverless.yml exists and serverless plugins are in package.json,
	// assume v3 (many projects install serverless globally or via CI)
	for _, section := range []string{"devDependencies", "dependencies"} {
		deps, ok := pkg[section].(map[string]interface{})
		if !ok {
			continue
		}
		for name := range deps {
			if strings.HasPrefix(name, "serverless-") {
				// Has serverless plugins → assume v3 (current standard)
				return 3
			}
		}
	}

	return 0
}

var (
	reServiceObject  = regexp.MustCompile(`(?m)^service:\s*\n(\s+name:\s*(.+))`)
	reResourcePolicy = regexp.MustCompile(`(?m)^(\s*)resourcePolicy:\s*\n`)
	reRuntimeLine    = regexp.MustCompile(`(?m)(runtime:\s*)nodejs(\d+)\.x`)
)

// applyServerlessYmlFixes applies all SLS v3 YAML fixes to the file.
func applyServerlessYmlFixes(repoPath string, slsFile string, targetNodeVersion int) (bool, error) {
	data, err := os.ReadFile(slsFile)
	if err != nil {
		return false, err
	}

	content := string(data)
	original := content

	// 1. Flatten service object notation: "service:\n  name: foo" → "service: foo"
	content = flattenServiceProperty(content)

	// 2. Cap runtime to nodejs20.x if target exceeds SLS v3 max
	content = capServerlessRuntime(content, targetNodeVersion)

	// 3. Remove serverless-pseudo-parameters from plugins list
	content = removeDeprecatedPlugin(content, "serverless-pseudo-parameters")

	// 4. Move top-level resourcePolicy under provider.apiGateway
	content = moveResourcePolicyToApiGateway(content)

	if content == original {
		return false, nil
	}

	return true, os.WriteFile(slsFile, []byte(content), 0644)
}

// flattenServiceProperty converts "service:\n  name: X" to "service: X".
func flattenServiceProperty(content string) string {
	m := reServiceObject.FindStringSubmatch(content)
	if m == nil {
		return content
	}
	serviceName := strings.TrimSpace(m[2])
	// Replace the multi-line service block with flat form
	return reServiceObject.ReplaceAllString(content, "service: "+serviceName)
}

// capServerlessRuntime ensures runtime doesn't exceed nodejs20.x for SLS v3.
func capServerlessRuntime(content string, targetNodeVersion int) string {
	if targetNodeVersion <= ServerlessV3MaxRuntime {
		return content
	}

	return reRuntimeLine.ReplaceAllStringFunc(content, func(match string) string {
		sub := reRuntimeLine.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		prefix := sub[1]
		// Cap to max supported version
		return fmt.Sprintf("%snodejs%d.x", prefix, ServerlessV3MaxRuntime)
	})
}

// removeDeprecatedPlugin removes a plugin from the plugins list in serverless.yml.
func removeDeprecatedPlugin(content string, pluginName string) string {
	lines := strings.Split(content, "\n")
	var filtered []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "- "+pluginName {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

// moveResourcePolicyToApiGateway moves provider-level resourcePolicy under provider.apiGateway.
// Detects when resourcePolicy is a direct child of provider (not already under apiGateway).
func moveResourcePolicyToApiGateway(content string) string {
	lines := strings.Split(content, "\n")

	// Find if resourcePolicy is at provider level (indentation = provider's children indent)
	providerIdx := -1
	providerIndent := -1
	resourcePolicyStart := -1
	resourcePolicyEnd := -1
	alreadyUnderApiGateway := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if trimmed == "provider:" {
			providerIdx = i
			providerIndent = indent
			continue
		}

		// Check if apiGateway already has resourcePolicy
		if trimmed == "apiGateway:" && providerIdx >= 0 {
			// Look ahead for resourcePolicy under apiGateway
			apiGwIndent := indent
			for j := i + 1; j < len(lines); j++ {
				jTrimmed := strings.TrimSpace(lines[j])
				jIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
				if jTrimmed == "" {
					continue
				}
				if jIndent <= apiGwIndent {
					break
				}
				if jTrimmed == "resourcePolicy:" || strings.HasPrefix(jTrimmed, "resourcePolicy:") {
					alreadyUnderApiGateway = true
					break
				}
			}
		}

		// Find resourcePolicy at provider child level (direct child of provider)
		if providerIdx >= 0 && providerIndent >= 0 && indent == providerIndent+2 {
			if trimmed == "resourcePolicy:" || strings.HasPrefix(trimmed, "resourcePolicy:") {
				resourcePolicyStart = i
				// Find the end of this block (all following lines with greater indent)
				for j := i + 1; j < len(lines); j++ {
					jTrimmed := strings.TrimSpace(lines[j])
					jIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
					if jTrimmed == "" {
						continue
					}
					if jIndent <= indent {
						resourcePolicyEnd = j
						break
					}
				}
				if resourcePolicyEnd == -1 {
					resourcePolicyEnd = len(lines)
				}
				break
			}
		}
	}

	if alreadyUnderApiGateway || resourcePolicyStart == -1 {
		return content
	}

	// Extract resourcePolicy block (copy to avoid slice mutation)
	rpBlock := make([]string, resourcePolicyEnd-resourcePolicyStart)
	copy(rpBlock, lines[resourcePolicyStart:resourcePolicyEnd])

	// Remove it from original position (safe copy)
	newLines := make([]string, 0, len(lines)-(resourcePolicyEnd-resourcePolicyStart))
	newLines = append(newLines, lines[:resourcePolicyStart]...)
	newLines = append(newLines, lines[resourcePolicyEnd:]...)

	// Find or create apiGateway section under provider
	apiGwIdx := -1
	providerChildIndent := providerIndent + 2
	apiGwChildIndent := providerChildIndent + 2

	for i, line := range newLines {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if trimmed == "apiGateway:" && indent == providerChildIndent {
			apiGwIdx = i
			break
		}
	}

	// Re-indent resourcePolicy block to be under apiGateway.
	// Calculate the shift: old position was providerChildIndent, new is apiGwChildIndent.
	// Just prepend the delta to every line to preserve relative indentation.
	oldIndent := providerChildIndent // where resourcePolicy: was
	newIndent := apiGwChildIndent    // where it should go
	delta := newIndent - oldIndent

	var reindented []string
	for _, line := range rpBlock {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			reindented = append(reindented, "")
		} else if delta >= 0 {
			reindented = append(reindented, strings.Repeat(" ", delta)+line)
		} else {
			// Negative delta: remove leading spaces (shouldn't happen normally)
			lineIndent := len(line) - len(strings.TrimLeft(line, " "))
			newLineIndent := lineIndent + delta
			if newLineIndent < 0 {
				newLineIndent = 0
			}
			reindented = append(reindented, strings.Repeat(" ", newLineIndent)+trimmed)
		}
	}

	if apiGwIdx == -1 {
		// Create apiGateway section right after provider line or after its existing children
		insertAt := providerIdx + 1
		for i := providerIdx + 1; i < len(newLines); i++ {
			trimmed := strings.TrimSpace(newLines[i])
			indent := len(newLines[i]) - len(strings.TrimLeft(newLines[i], " "))
			if trimmed == "" {
				continue
			}
			if indent <= providerIndent {
				break
			}
			insertAt = i + 1
		}
		apiGwLine := strings.Repeat(" ", providerChildIndent) + "apiGateway:"
		insertion := append([]string{apiGwLine}, reindented...)
		result := make([]string, 0, len(newLines)+len(insertion))
		result = append(result, newLines[:insertAt]...)
		result = append(result, insertion...)
		result = append(result, newLines[insertAt:]...)
		return strings.Join(result, "\n")
	}

	// Insert after apiGateway's existing children
	insertAt := apiGwIdx + 1
	for i := apiGwIdx + 1; i < len(newLines); i++ {
		trimmed := strings.TrimSpace(newLines[i])
		indent := len(newLines[i]) - len(strings.TrimLeft(newLines[i], " "))
		if trimmed == "" {
			continue
		}
		if indent <= providerChildIndent {
			break
		}
		insertAt = i + 1
	}

	result := make([]string, 0, len(newLines)+len(reindented))
	result = append(result, newLines[:insertAt]...)
	result = append(result, reindented...)
	result = append(result, newLines[insertAt:]...)
	return strings.Join(result, "\n")
}

// upgradeServerlessPlugins upgrades incompatible Serverless plugin versions in package.json.
func upgradeServerlessPlugins(repoPath string) (bool, error) {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return false, nil
	}

	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false, err
	}

	modified := false

	// Plugin version requirements for SLS v3 compatibility
	pluginUpgrades := map[string]string{
		"serverless-step-functions": "^3.21.0",
		"serverless-offline":        "^14.0.0",
	}

	for _, section := range []string{"dependencies", "devDependencies"} {
		deps, ok := pkg[section].(map[string]interface{})
		if !ok {
			continue
		}

		for plugin, minVersion := range pluginUpgrades {
			existing, ok := deps[plugin].(string)
			if !ok {
				continue
			}
			if needsUpgrade(existing, minVersion) {
				deps[plugin] = minVersion
				modified = true
			}
		}

		// Remove serverless-pseudo-parameters from package.json
		if _, ok := deps["serverless-pseudo-parameters"]; ok {
			delete(deps, "serverless-pseudo-parameters")
			modified = true
		}

		pkg[section] = deps
	}

	if !modified {
		return false, nil
	}

	out, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(pkgPath, append(out, '\n'), 0644)
}

// needsUpgrade checks if the current version is below the minimum required.
// Simple heuristic: compares major version from semver strings.
func needsUpgrade(current, minimum string) bool {
	currentMajor := extractMajor(current)
	minimumMajor := extractMajor(minimum)
	if currentMajor == 0 || minimumMajor == 0 {
		return false
	}
	return currentMajor < minimumMajor
}

// extractMajor extracts the major version number from a semver-like string.
func extractMajor(ver string) int {
	ver = strings.TrimLeft(ver, "^~>=<")
	major := 0
	for _, ch := range ver {
		if ch >= '0' && ch <= '9' {
			major = major*10 + int(ch-'0')
		} else {
			break
		}
	}
	return major
}
