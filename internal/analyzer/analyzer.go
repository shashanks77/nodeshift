package analyzer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	types "github.com/your-org/nodeshift/internal"
)

var reVersion = regexp.MustCompile(`(\d+)`)

var nativeModules = map[string]string{
	"xml2json":           "Uses node-expat native binding. Replace with fast-xml-parser or xml2js.",
	"xml-to-json-stream": "Uses node-expat native binding. Replace with fast-xml-parser.",
	"node-expat":         "Native XML parser. Replace with fast-xml-parser.",
	"node-sass":          "Deprecated native module. Replace with sass (dart-sass).",
	"bcrypt":             "Native module — ensure rebuilt for target Node ABI.",
	"sharp":              "Native module — usually supports latest Node but needs rebuild.",
	"canvas":             "Native module — check node-canvas compatibility.",
	"grpc":               "Legacy native module. Migrate to @grpc/grpc-js.",
	"sqlite3":            "Native module — ensure compatible version.",
	"better-sqlite3":     "Native module — usually keeps up with Node releases.",
}

var eolPackages = map[string]struct {
	Reason      string
	Replacement string
}{
	"aws-sdk": {
		Reason:      "AWS SDK v2 is end-of-support (archived Sep 2025). No Node 20+ guarantees.",
		Replacement: "@aws-sdk/* (v3 modular clients)",
	},
	"request": {
		Reason:      "Deprecated since Feb 2020.",
		Replacement: "axios, undici, or native fetch",
	},
	"tslint": {
		Reason:      "Deprecated. No longer maintained.",
		Replacement: "eslint + @typescript-eslint",
	},
	"serverless-pseudo-parameters": {
		Reason:      "Deprecated. Serverless Framework v3+ supports #{AWS::*} natively as ${aws:*}.",
		Replacement: "Remove plugin and use ${aws:accountId}, ${aws:partition}, etc.",
	},
}

// outdatedPackages tracks packages where a major version bump is needed for Node compat
var outdatedPackages = map[string]struct {
	MinMajor     int
	Reason       string
	SuggestedVer string
}{
	"nodemon":            {MinMajor: 3, Reason: "nodemon 2.x has compatibility issues with Node 20+. Upgrade to 3.x.", SuggestedVer: "^3.1.0"},
	"serverless-offline": {MinMajor: 14, Reason: "serverless-offline <14 incompatible with Serverless v3+ and Node 20+.", SuggestedVer: "^14.0.0"},
	"nats":               {MinMajor: 2, Reason: "nats v1.x is deprecated and unmaintained. v2.x uses a completely new API (JetStream, no callbacks).", SuggestedVer: "^2.28.0"},
	"moleculer":          {MinMajor: 1, Reason: "moleculer 0.14.x is outdated and unmaintained. 0.15+ has Node 20+ fixes and ESM support.", SuggestedVer: "^0.15.0"},
	"@nestjs/config":     {MinMajor: 3, Reason: "@nestjs/config <3.x uses util.isObject() removed in Node 18+. Causes TypeError at runtime.", SuggestedVer: "^3.2.0"},
	"@nestjs/core":       {MinMajor: 10, Reason: "@nestjs/core <10.x has compatibility issues with Node 20+. Upgrade to 10.x+.", SuggestedVer: "^10.3.0"},
	"@nestjs/common":     {MinMajor: 10, Reason: "@nestjs/common <10.x has compatibility issues with Node 20+. Upgrade to 10.x+.", SuggestedVer: "^10.3.0"},
	"@nestjs/typeorm":    {MinMajor: 10, Reason: "@nestjs/typeorm <10.x has compatibility issues with Node 20+. Upgrade to 10.x+.", SuggestedVer: "^10.0.0"},
	"@nestjs/swagger":    {MinMajor: 7, Reason: "@nestjs/swagger <7.x has compatibility issues with Node 20+. Upgrade to 7.x+.", SuggestedVer: "^7.3.0"},
}

func Analyze(repoPath string, targetVersion int) ([]types.DependencyIssue, error) {
	return AnalyzeWithOptions(repoPath, targetVersion, true)
}

// AnalyzeWithOptions performs dependency analysis. If upgradeDeps is true, queries the npm
// registry for latest versions and flags all outdated packages.
func AnalyzeWithOptions(repoPath string, targetVersion int, upgradeDeps bool) ([]types.DependencyIssue, error) {
	var issues []types.DependencyIssue

	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return issues, nil
	}

	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return issues, nil
	}

	allDeps := merge(pkg.Dependencies, pkg.DevDependencies)

	for name, version := range allDeps {
		if reason, ok := nativeModules[name]; ok {
			issues = append(issues, types.DependencyIssue{
				Name:           name,
				CurrentVersion: version,
				Issue:          "native-module",
				Severity:       "high",
				Reason:         reason,
			})
		}
		if eol, ok := eolPackages[name]; ok {
			issues = append(issues, types.DependencyIssue{
				Name:             name,
				CurrentVersion:   version,
				Issue:            "eol",
				Severity:         "high",
				Reason:           eol.Reason,
				SuggestedVersion: eol.Replacement,
			})
		}
	}

	if ver, ok := allDeps["webpack"]; ok {
		if major := extractMajor(ver); major > 0 && major < 5 && targetVersion > 16 {
			issues = append(issues, types.DependencyIssue{
				Name:             "webpack",
				CurrentVersion:   ver,
				Issue:            "incompatible",
				Severity:         "high",
				Reason:           "Webpack 4 fails with ERR_OSSL_EVP_UNSUPPORTED on Node 17+ due to OpenSSL 3.",
				SuggestedVersion: "Upgrade to webpack 5.",
			})
		}
	}

	if ver, ok := allDeps["typescript"]; ok {
		major := extractMajor(ver)
		// Node 24+ requires TypeScript 6.x (for @tsconfig/node24 es2024/es2025 lib support)
		if targetVersion >= 24 && major < 6 {
			issues = append(issues, types.DependencyIssue{
				Name:             "typescript",
				CurrentVersion:   ver,
				Issue:            "incompatible",
				Severity:         "high",
				Reason:           "TypeScript " + strconv.Itoa(major) + ".x does not support es2024/es2025 lib targets required by @tsconfig/node" + strconv.Itoa(targetVersion) + ". Upgrade to 6.x.",
				SuggestedVersion: "^6.0.0",
			})
		} else if major > 0 && major < 5 {
			issues = append(issues, types.DependencyIssue{
				Name:             "typescript",
				CurrentVersion:   ver,
				Issue:            "incompatible",
				Severity:         "medium",
				Reason:           "TypeScript " + strconv.Itoa(major) + ".x lacks Node " + strconv.Itoa(targetVersion) + " type definitions. Upgrade to 5.x.",
				SuggestedVersion: "^5.4.0",
			})
		}
	}

	if ver, ok := allDeps["jest"]; ok {
		if major := extractMajor(ver); major > 0 && major < 29 {
			issues = append(issues, types.DependencyIssue{
				Name:             "jest",
				CurrentVersion:   ver,
				Issue:            "incompatible",
				Severity:         "medium",
				Reason:           "Jest " + strconv.Itoa(major) + ".x may have issues on Node " + strconv.Itoa(targetVersion) + ". Upgrade to 29.x.",
				SuggestedVersion: "^29.7.0",
			})
		}
	}

	if ver, ok := allDeps["@types/node"]; ok {
		if major := extractMajor(ver); major > 0 && major < targetVersion {
			issues = append(issues, types.DependencyIssue{
				Name:             "@types/node",
				CurrentVersion:   ver,
				Issue:            "incompatible",
				Severity:         "low",
				Reason:           "@types/node should match target Node version.",
				SuggestedVersion: "^" + strconv.Itoa(targetVersion) + ".0.0",
			})
		}
	}

	// Check for outdated packages that need major version bumps
	for name, info := range outdatedPackages {
		if ver, ok := allDeps[name]; ok {
			major := extractMajor(ver)
			if major < info.MinMajor {
				issues = append(issues, types.DependencyIssue{
					Name:             name,
					CurrentVersion:   ver,
					Issue:            "outdated",
					Severity:         "medium",
					Reason:           info.Reason,
					SuggestedVersion: info.SuggestedVer,
				})
			}
		}
	}

	// Check for @tsconfig/nodeXX that doesn't match target
	reTsconfigPkg := regexp.MustCompile(`^@tsconfig/node(\d+)$`)
	for name, ver := range allDeps {
		if m := reTsconfigPkg.FindStringSubmatch(name); len(m) == 2 {
			nodeVer, _ := strconv.Atoi(m[1])
			if nodeVer < targetVersion {
				issues = append(issues, types.DependencyIssue{
					Name:             name,
					CurrentVersion:   ver,
					Issue:            "incompatible",
					Severity:         "low",
					Reason:           name + " should match target Node version. Replace with @tsconfig/node" + strconv.Itoa(targetVersion) + ".",
					SuggestedVersion: "@tsconfig/node" + strconv.Itoa(targetVersion),
				})
			}
		}
	}

	// Phase 2: Fetch latest versions from npm registry and flag all outdated deps
	if upgradeDeps {
		fmt.Println("  [LLM-ANALYZE] Fetching latest versions from npm registry...")
		latestVersions := FetchLatestVersions(allDeps)

		// Track which packages already have issues (don't duplicate)
		alreadyFlagged := make(map[string]bool)
		for _, issue := range issues {
			alreadyFlagged[issue.Name] = true
		}

		for name, currentVer := range allDeps {
			if alreadyFlagged[name] {
				continue
			}
			latestVer, ok := latestVersions[name]
			if !ok {
				continue
			}
			bump := CompareVersions(currentVer, latestVer)
			if bump == "" {
				continue
			}

			severity := "low"
			if bump == "major" {
				severity = "high"
			} else if bump == "minor" {
				severity = "medium"
			}

			issues = append(issues, types.DependencyIssue{
				Name:             name,
				CurrentVersion:   currentVer,
				Issue:            "outdated-latest",
				Severity:         severity,
				Reason:           fmt.Sprintf("%s %s is outdated. Latest stable: %s (%s version bump).", name, currentVer, latestVer, bump),
				SuggestedVersion: "^" + latestVer,
			})
		}
		fmt.Printf("  [LLM-ANALYZE] Found %d packages needing upgrade\n", len(issues))
	}

	return issues, nil
}

func extractMajor(version string) int {
	m := reVersion.FindStringSubmatch(version)
	if len(m) >= 2 {
		v, _ := strconv.Atoi(m[1])
		return v
	}
	return 0
}

func merge(maps ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}
