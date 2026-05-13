package transformer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	types "github.com/your-org/nodeshift/internal"
)

func Transform(repoPath string, configs []types.DetectedNodeConfig, targetVersion int, issues ...[]types.DependencyIssue) ([]string, error) {
	var changed []string

	for _, cfg := range configs {
		ok, err := transformConfig(repoPath, cfg, targetVersion)
		if err != nil {
			return changed, fmt.Errorf("transform %s: %w", cfg.File, err)
		}
		if ok {
			changed = append(changed, cfg.File)
		}
	}

	// Upgrade package.json dependencies if issues provided
	if len(issues) > 0 && len(issues[0]) > 0 {
		ok, err := TransformPackageDeps(repoPath, issues[0], targetVersion)
		if err != nil {
			return changed, fmt.Errorf("transform package.json: %w", err)
		}
		if ok {
			changed = append(changed, "package.json")
		}
	}

	return changed, nil
}

func transformConfig(repoPath string, cfg types.DetectedNodeConfig, target int) (bool, error) {
	switch cfg.Type {
	case "serverless":
		return transformServerless(repoPath, cfg, target)
	case "dockerfile":
		return transformDockerfile(repoPath, cfg, target)
	case "nvmrc", "node-version":
		return transformSimpleFile(repoPath, cfg, target)
	case "package-engines":
		return transformPackageEngines(repoPath, cfg, target)
	case "ci-workflow":
		return transformCIWorkflow(repoPath, cfg, target)
	case "tsconfig-target":
		return transformTsconfigTarget(repoPath, cfg, target)
	case "serverless-pseudo-params":
		return transformServerlessPseudoParams(repoPath, cfg)
	}
	return false, nil
}

func transformServerless(repoPath string, cfg types.DetectedNodeConfig, target int) (bool, error) {
	fullPath := filepath.Join(repoPath, cfg.File)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, err
	}

	old := "nodejs" + cfg.CurrentVersion + ".x"
	newRuntime := "nodejs" + strconv.Itoa(target) + ".x"
	content := string(data)

	if !strings.Contains(content, old) {
		return false, nil
	}

	re := regexp.MustCompile(`runtime:\s*` + regexp.QuoteMeta(old))
	updated := re.ReplaceAllString(content, "runtime: "+newRuntime)

	return true, os.WriteFile(fullPath, []byte(updated), 0644)
}

func transformDockerfile(repoPath string, cfg types.DetectedNodeConfig, target int) (bool, error) {
	fullPath := filepath.Join(repoPath, cfg.File)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, err
	}

	re := regexp.MustCompile(`(?i)(FROM\s+node:)(\d+)`)
	content := string(data)
	updated := re.ReplaceAllString(content, "${1}"+strconv.Itoa(target))

	if updated == content {
		return false, nil
	}
	return true, os.WriteFile(fullPath, []byte(updated), 0644)
}

func transformSimpleFile(repoPath string, cfg types.DetectedNodeConfig, target int) (bool, error) {
	fullPath := filepath.Join(repoPath, cfg.File)
	return true, os.WriteFile(fullPath, []byte(strconv.Itoa(target)+"\n"), 0644)
}

func transformPackageEngines(repoPath string, cfg types.DetectedNodeConfig, target int) (bool, error) {
	fullPath := filepath.Join(repoPath, cfg.File)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, err
	}

	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false, nil
	}

	engines, ok := pkg["engines"].(map[string]interface{})
	if !ok {
		return false, nil
	}

	engines["node"] = fmt.Sprintf(">=%d.0.0", target)
	pkg["engines"] = engines

	out, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(fullPath, append(out, '\n'), 0644)
}

func transformCIWorkflow(repoPath string, cfg types.DetectedNodeConfig, target int) (bool, error) {
	fullPath := filepath.Join(repoPath, cfg.File)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, err
	}

	re := regexp.MustCompile(`node-version:\s*['"]?\d+['"]?`)
	content := string(data)
	updated := re.ReplaceAllString(content, fmt.Sprintf("node-version: '%d'", target))

	if updated == content {
		return false, nil
	}
	return true, os.WriteFile(fullPath, []byte(updated), 0644)
}

// nodeVersionToESTarget maps Node major version to the best ES target
func nodeVersionToESTarget(nodeVersion int) string {
	switch {
	case nodeVersion >= 24:
		return "ES2024"
	case nodeVersion >= 22:
		return "ES2023"
	case nodeVersion >= 20:
		return "ES2022"
	case nodeVersion >= 18:
		return "ES2022"
	case nodeVersion >= 16:
		return "ES2021"
	default:
		return "ES2020"
	}
}

func transformTsconfigTarget(repoPath string, cfg types.DetectedNodeConfig, target int) (bool, error) {
	fullPath := filepath.Join(repoPath, cfg.File)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, err
	}

	newTarget := nodeVersionToESTarget(target)
	content := string(data)

	// Replace the target value, preserving surrounding JSON structure
	re := regexp.MustCompile(`("target"\s*:\s*)"(?:ES\d+|es\d+|ESNext|esnext)"`)
	updated := re.ReplaceAllString(content, `${1}"`+newTarget+`"`)

	if updated == content {
		return false, nil
	}
	return true, os.WriteFile(fullPath, []byte(updated), 0644)
}

func transformServerlessPseudoParams(repoPath string, cfg types.DetectedNodeConfig) (bool, error) {
	fullPath := filepath.Join(repoPath, cfg.File)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, err
	}

	content := string(data)

	// Map #{AWS::X} to ${aws:x} (Serverless Framework v3+ native syntax)
	replacements := map[string]string{
		"#{AWS::AccountId}": "${aws:accountId}",
		"#{AWS::Region}":    "${self:provider.region}",
		"#{AWS::Partition}": "${aws:partition}",
		"#{AWS::StackName}": "${aws:stackName}",
		"#{AWS::StackId}":   "${aws:stackId}",
	}

	updated := content
	for old, newVal := range replacements {
		updated = strings.ReplaceAll(updated, old, newVal)
	}

	// Remove serverless-pseudo-parameters plugin line
	lines := strings.Split(updated, "\n")
	var filtered []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "- serverless-pseudo-parameters" {
			continue
		}
		filtered = append(filtered, line)
	}
	updated = strings.Join(filtered, "\n")

	if updated == content {
		return false, nil
	}
	return true, os.WriteFile(fullPath, []byte(updated), 0644)
}

// DepUpgrade defines a dependency replacement rule.
type DepUpgrade struct {
	Remove  []string          // packages to remove
	Add     map[string]string // packages to add (name -> version)
	Upgrade map[string]string // packages to upgrade in-place (name -> new version)
}

// TransformPackageDeps upgrades dependencies in package.json based on detected issues.
func TransformPackageDeps(repoPath string, issues []types.DependencyIssue, target int) (bool, error) {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return false, nil // no package.json, skip
	}

	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false, err
	}

	deps, _ := pkg["dependencies"].(map[string]interface{})
	devDeps, _ := pkg["devDependencies"].(map[string]interface{})
	if deps == nil {
		deps = make(map[string]interface{})
	}
	if devDeps == nil {
		devDeps = make(map[string]interface{})
	}

	modified := false

	for _, issue := range issues {
		switch issue.Name {
		case "aws-sdk":
			// Remove aws-sdk, add v3 modular clients based on what's actually used in code
			if _, ok := deps["aws-sdk"]; ok {
				delete(deps, "aws-sdk")
				// Add v3 clients - detect which ones are needed from source
				v3Deps := detectRequiredAwsV3Packages(repoPath)
				for pkg, ver := range v3Deps {
					deps[pkg] = ver
				}
				modified = true
			}

		case "webpack":
			if _, ok := deps["webpack"]; ok {
				deps["webpack"] = "^5.90.0"
				modified = true
			}
			if _, ok := devDeps["webpack"]; ok {
				devDeps["webpack"] = "^5.90.0"
				modified = true
			}
			// ts-loader must be v9+ for webpack 5
			if _, ok := devDeps["ts-loader"]; ok {
				devDeps["ts-loader"] = "^9.5.0"
				modified = true
			}

		case "typescript":
			if _, ok := devDeps["typescript"]; ok {
				devDeps["typescript"] = "^5.4.0"
				modified = true
			}

		case "jest":
			if _, ok := devDeps["jest"]; ok {
				devDeps["jest"] = "^29.7.0"
				modified = true
			}
			if _, ok := devDeps["ts-jest"]; ok {
				devDeps["ts-jest"] = "^29.1.0"
				modified = true
			}
			if _, ok := devDeps["@types/jest"]; ok {
				devDeps["@types/jest"] = "^29.5.0"
				modified = true
			}

		case "tslint":
			delete(devDeps, "tslint")
			delete(devDeps, "tslint-config-prettier")
			if _, ok := devDeps["eslint"]; !ok {
				devDeps["eslint"] = "^8.57.0"
			}
			devDeps["@typescript-eslint/parser"] = "^7.0.0"
			devDeps["@typescript-eslint/eslint-plugin"] = "^7.0.0"
			modified = true

		case "xml2json":
			if _, ok := deps["xml2json"]; ok {
				delete(deps, "xml2json")
				deps["fast-xml-parser"] = "^4.3.0"
				modified = true
			}

		case "@types/node":
			if _, ok := devDeps["@types/node"]; ok {
				devDeps["@types/node"] = fmt.Sprintf("^%d.0.0", target)
				modified = true
			}

		case "nodemon":
			if v, ok := deps["nodemon"]; ok {
				_ = v
				deps["nodemon"] = "^3.1.0"
				modified = true
			}
			if v, ok := devDeps["nodemon"]; ok {
				_ = v
				devDeps["nodemon"] = "^3.1.0"
				modified = true
			}

		case "serverless-offline":
			if v, ok := devDeps["serverless-offline"]; ok {
				_ = v
				devDeps["serverless-offline"] = "^14.0.0"
				modified = true
			}

		case "serverless-pseudo-parameters":
			// Remove the deprecated plugin entirely
			delete(deps, "serverless-pseudo-parameters")
			delete(devDeps, "serverless-pseudo-parameters")
			modified = true
		}
	}

	if !modified {
		return false, nil
	}

	// Ensure @typescript-eslint/parser and eslint-plugin versions are in sync
	if parserVer, ok := devDeps["@typescript-eslint/parser"]; ok {
		if pluginVer, ok2 := devDeps["@typescript-eslint/eslint-plugin"]; ok2 {
			if parserVer != pluginVer {
				devDeps["@typescript-eslint/eslint-plugin"] = parserVer
			}
		}
	}

	pkg["dependencies"] = deps
	pkg["devDependencies"] = devDeps

	out, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(pkgPath, append(out, '\n'), 0644)
}

// detectRequiredAwsV3Packages scans source files to determine which @aws-sdk packages are needed.
func detectRequiredAwsV3Packages(repoPath string) map[string]string {
	pkgs := make(map[string]string)

	// Walk source files looking for AWS service usage
	srcDir := filepath.Join(repoPath, "src")
	filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".js") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)

		if strings.Contains(content, "DynamoDB") {
			pkgs["@aws-sdk/client-dynamodb"] = "^3.600.0"
			pkgs["@aws-sdk/lib-dynamodb"] = "^3.600.0"
		}
		if strings.Contains(content, "SQS") || strings.Contains(content, "sendMessage") {
			pkgs["@aws-sdk/client-sqs"] = "^3.600.0"
		}
		if strings.Contains(content, "SNS") || strings.Contains(content, "publish") {
			pkgs["@aws-sdk/client-sns"] = "^3.600.0"
		}
		if strings.Contains(content, "StepFunctions") || strings.Contains(content, "startExecution") || strings.Contains(content, "SFNClient") || strings.Contains(content, "@aws-sdk/client-sfn") {
			pkgs["@aws-sdk/client-sfn"] = "^3.600.0"
		}
		if strings.Contains(content, "S3") {
			pkgs["@aws-sdk/client-s3"] = "^3.600.0"
		}
		if strings.Contains(content, "SecretsManager") {
			pkgs["@aws-sdk/client-secrets-manager"] = "^3.600.0"
		}
		if strings.Contains(content, "Lambda") {
			pkgs["@aws-sdk/client-lambda"] = "^3.600.0"
		}

		return nil
	})

	return pkgs
}
