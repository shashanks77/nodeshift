package detector

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	types "github.com/your-org/nodeshift/internal"
)

var (
	reServerlessRuntime    = regexp.MustCompile(`runtime:\s*(nodejs(\d+)\.x)`)
	reDockerFrom          = regexp.MustCompile(`(?i)FROM\s+node:(\d+)`)
	reNodeVersion         = regexp.MustCompile(`v?(\d+)`)
	reCINodeVersion       = regexp.MustCompile(`node-version:\s*['"]?(\d+)`)
	reTsconfigTarget      = regexp.MustCompile(`"target"\s*:\s*"(ES\d+|es\d+|ESNext|esnext)"`) 
	rePseudoParam         = regexp.MustCompile(`#\{AWS::(\w+)\}`)
)

func Scan(repoPath string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig

	detectors := []func(string) ([]types.DetectedNodeConfig, error){
		detectServerless,
		detectDockerfiles,
		detectNvmrc,
		detectNodeVersionFile,
		detectPackageEngines,
		detectGitHubActions,
		detectTsconfig,
		detectServerlessPseudoParams,
	}

	for _, fn := range detectors {
		found, err := fn(repoPath)
		if err != nil {
			return nil, err
		}
		configs = append(configs, found...)
	}

	return configs, nil
}

func detectServerless(repoPath string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig
	patterns := []string{"serverless.yml", "serverless.yaml"}
	files := findFiles(repoPath, patterns)

	for _, file := range files {
		matches := scanFileRegex(file, reServerlessRuntime)
		for _, m := range matches {
			if len(m.groups) >= 2 {
				rel, _ := filepath.Rel(repoPath, file)
				configs = append(configs, types.DetectedNodeConfig{
					File:           rel,
					Type:           "serverless",
					CurrentVersion: m.groups[1],
					Line:           m.line,
				})
			}
		}
	}
	return configs, nil
}

func detectDockerfiles(repoPath string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig
	_ = filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && info.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.Contains(info.Name(), "Dockerfile") {
			matches := scanFileRegex(path, reDockerFrom)
			for _, m := range matches {
				if len(m.groups) >= 1 {
					rel, _ := filepath.Rel(repoPath, path)
					configs = append(configs, types.DetectedNodeConfig{
						File:           rel,
						Type:           "dockerfile",
						CurrentVersion: m.groups[0],
						Line:           m.line,
					})
				}
			}
		}
		return nil
	})
	return configs, nil
}

func detectNvmrc(repoPath string) ([]types.DetectedNodeConfig, error) {
	return detectSimpleVersionFile(repoPath, ".nvmrc", "nvmrc")
}

func detectNodeVersionFile(repoPath string) ([]types.DetectedNodeConfig, error) {
	return detectSimpleVersionFile(repoPath, ".node-version", "node-version")
}

func detectSimpleVersionFile(repoPath, filename, configType string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig
	_ = filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && info.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if !info.IsDir() && info.Name() == filename {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			match := reNodeVersion.FindStringSubmatch(strings.TrimSpace(string(data)))
			if len(match) >= 2 {
				rel, _ := filepath.Rel(repoPath, path)
				configs = append(configs, types.DetectedNodeConfig{
					File:           rel,
					Type:           configType,
					CurrentVersion: match[1],
				})
			}
		}
		return nil
	})
	return configs, nil
}

func detectPackageEngines(repoPath string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig
	_ = filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && info.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if !info.IsDir() && info.Name() == "package.json" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var pkg struct {
				Engines struct {
					Node string `json:"node"`
				} `json:"engines"`
			}
			if err := json.Unmarshal(data, &pkg); err != nil {
				return nil
			}
			if pkg.Engines.Node != "" {
				match := reNodeVersion.FindStringSubmatch(pkg.Engines.Node)
				if len(match) >= 2 {
					rel, _ := filepath.Rel(repoPath, path)
					configs = append(configs, types.DetectedNodeConfig{
						File:           rel,
						Type:           "package-engines",
						CurrentVersion: match[1],
					})
				}
			}
		}
		return nil
	})
	return configs, nil
}

func detectGitHubActions(repoPath string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig
	workflowDir := filepath.Join(repoPath, ".github", "workflows")
	if _, err := os.Stat(workflowDir); os.IsNotExist(err) {
		return configs, nil
	}

	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return configs, nil
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		fullPath := filepath.Join(workflowDir, name)
		matches := scanFileRegex(fullPath, reCINodeVersion)
		for _, m := range matches {
			if len(m.groups) >= 1 {
				rel, _ := filepath.Rel(repoPath, fullPath)
				configs = append(configs, types.DetectedNodeConfig{
					File:           rel,
					Type:           "ci-workflow",
					CurrentVersion: m.groups[0],
					Line:           m.line,
				})
			}
		}
	}
	return configs, nil
}

func detectTsconfig(repoPath string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig
	// Only check root tsconfig.json
	tscPath := filepath.Join(repoPath, "tsconfig.json")
	if _, err := os.Stat(tscPath); os.IsNotExist(err) {
		return configs, nil
	}
	matches := scanFileRegex(tscPath, reTsconfigTarget)
	for _, m := range matches {
		if len(m.groups) >= 1 {
			configs = append(configs, types.DetectedNodeConfig{
				File:           "tsconfig.json",
				Type:           "tsconfig-target",
				CurrentVersion: m.groups[0],
				Line:           m.line,
			})
		}
	}
	return configs, nil
}

func detectServerlessPseudoParams(repoPath string) ([]types.DetectedNodeConfig, error) {
	var configs []types.DetectedNodeConfig
	patterns := []string{"serverless.yml", "serverless.yaml"}
	files := findFiles(repoPath, patterns)
	for _, file := range files {
		matches := scanFileRegex(file, rePseudoParam)
		if len(matches) > 0 {
			rel, _ := filepath.Rel(repoPath, file)
			configs = append(configs, types.DetectedNodeConfig{
				File:           rel,
				Type:           "serverless-pseudo-params",
				CurrentVersion: "#{AWS::*}",
				Line:           matches[0].line,
			})
		}
	}
	return configs, nil
}

type regexMatch struct {
	line   int
	groups []string
}

func scanFileRegex(path string, re *regexp.Regexp) []regexMatch {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var matches []regexMatch
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		m := re.FindStringSubmatch(line)
		if m != nil {
			matches = append(matches, regexMatch{line: lineNum, groups: m[1:]})
		}
	}
	return matches
}

func findFiles(root string, patterns []string) []string {
	var results []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err == nil {
			results = append(results, matches...)
		}
	}
	seen := make(map[string]bool)
	var deduped []string
	for _, f := range results {
		if !seen[f] {
			seen[f] = true
			deduped = append(deduped, f)
		}
	}
	return deduped
}
