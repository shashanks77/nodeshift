package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	types "github.com/your-org/nodeshift/internal"
	"github.com/your-org/nodeshift/internal/analyzer"
	"github.com/your-org/nodeshift/internal/detector"
	ghclient "github.com/your-org/nodeshift/internal/github"
	"github.com/your-org/nodeshift/internal/llm"
	"github.com/your-org/nodeshift/internal/transformer"
	"github.com/your-org/nodeshift/internal/verify"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "nodeshift",
		Short: "Automated Node.js version upgrade agent",
	}

	rootCmd.AddCommand(scanCmd())
	rootCmd.AddCommand(upgradeCmd())
	rootCmd.AddCommand(batchCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func scanCmd() *cobra.Command {
	var (
		local  string
		owner  string
		repo   string
		target int
		token  string
	)

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan a repo for Node version configs and analyze compatibility",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}

			repoPath := local
			if repoPath == "" {
				if token == "" || owner == "" || repo == "" {
					return fmt.Errorf("provide --local or --owner + --repo + --token")
				}
				gh := ghclient.New(token, owner, repo, "main", target, true, "/tmp")
				var err error
				repoPath, err = gh.Clone()
				if err != nil {
					return err
				}
			}

			configs, err := detector.Scan(repoPath)
			if err != nil {
				return err
			}

			issues, err := analyzer.Analyze(repoPath, target)
			if err != nil {
				return err
			}

			printReport(owner+"/"+repo, configs, issues, nil, target)
			return nil
		},
	}

	cmd.Flags().StringVarP(&local, "local", "l", "", "Scan a local directory")
	cmd.Flags().StringVarP(&owner, "owner", "o", "", "GitHub owner/org")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "GitHub repo name")
	cmd.Flags().IntVarP(&target, "target", "t", 24, "Target Node.js major version")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token")

	return cmd
}

func upgradeCmd() *cobra.Command {
	var (
		target     int
		baseBranch string
		token      string
		dryRun     bool
		llmURL     string
		llmModel   string
	)

	cmd := &cobra.Command{
		Use:   "upgrade [repo-url-or-path]",
		Short: "Upgrade Node version in a repo and raise a PR",
		Long: `Upgrade a repository's Node.js version. Accepts either:
  - A GitHub URL: https://github.com/org/repo.git (clones, upgrades, raises PR)
  - A local path: ./my-repo or /path/to/repo (upgrades in place, raises PR on remote)
  
Use --dry-run to preview changes without pushing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoInput := args[0]

			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}

			// Determine if input is a URL or local path
			isURL := strings.HasPrefix(repoInput, "https://") || strings.HasPrefix(repoInput, "git@")

			var owner, repo, repoPath string
			var isLocal bool

			if isURL {
				// Parse owner/repo from URL
				owner, repo = parseGitHubURL(repoInput)
				if owner == "" || repo == "" {
					return fmt.Errorf("could not parse owner/repo from URL: %s", repoInput)
				}
				if token == "" {
					return fmt.Errorf("GITHUB_TOKEN required for remote repos (set in .env or environment)")
				}
			} else {
				// Local path — resolve and read remote
				isLocal = true
				repoPath = repoInput
				// Make relative paths absolute
				if !strings.HasPrefix(repoPath, "/") {
					cwd, _ := os.Getwd()
					repoPath = cwd + "/" + repoPath
				}
				// Read remote URL from git
				remoteURL := getGitRemoteURL(repoPath)
				if remoteURL != "" {
					owner, repo = parseGitHubURL(remoteURL)
				}
				if owner == "" || repo == "" {
					if !dryRun {
						fmt.Println("  [WARN] Could not determine GitHub remote. Running in local-only mode (no PR).")
					}
				} else if token == "" {
					fmt.Println("  [WARN] No GITHUB_TOKEN set. Running in local-only mode (no PR).")
					owner = ""
					repo = ""
				}
			}

			canPR := owner != "" && repo != "" && token != "" && !dryRun

			gh := ghclient.New(token, owner, repo, baseBranch, target, dryRun, "/tmp/upgrade-work")

			if !isLocal {
				var err error
				repoPath, err = gh.Clone()
				if err != nil {
					return err
				}
			} else {
				// Reset local repo to clean state
				resetCmd := fmt.Sprintf("cd %s && git checkout -- .", repoPath)
				exec := runShell(resetCmd)
				if exec != nil {
					// ignore reset errors
				}
			}

			configs, err := detector.Scan(repoPath)
			if err != nil {
				return err
			}

			// Auto-detect latest stable Node version if target is 0
			if target == 0 {
				fmt.Println("  [DETECT] Fetching latest stable Node.js version...")
				latest, err := analyzer.FetchLatestStableNode()
				if err != nil {
					return fmt.Errorf("auto-detect node version: %w", err)
				}
				target = latest
				fmt.Printf("  [DETECT] Latest stable: Node.js %d\n", target)
			}

			// Static analysis
			issues, err := analyzer.Analyze(repoPath, target)
			if err != nil {
				return err
			}

			// LLM-based analysis (merges with static analysis)
			if llmURL != "" {
				llmClient := llm.NewClient(llmURL, llmModel)
				if err := llmClient.Ping(); err == nil {
					llmIssues, _ := analyzer.AnalyzeWithLLM(llmClient, repoPath, target)
					if len(llmIssues) > 0 {
						// Merge: add LLM issues that aren't already detected statically
						existing := make(map[string]bool)
						for _, i := range issues {
							existing[i.Name] = true
						}
						for _, li := range llmIssues {
							if !existing[li.Name] {
								issues = append(issues, li)
							}
						}
					}
				}
			}

			var branch string
			if canPR {
				branch, err = gh.CreateBranch(repoPath)
				if err != nil {
					return err
				}
			}

			var filesChanged []string

			// Phase 1: Config transforms + package.json upgrades
			configChanged, err := transformer.Transform(repoPath, configs, target, issues)
			if err != nil {
				return err
			}
			filesChanged = append(filesChanged, configChanged...)

			// Phase 1b: Serverless Framework v3 compatibility fixes
			slsChanged, err := transformer.TransformServerlessV3Compat(repoPath, target)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [WARN] Serverless v3 compat: %v\n", err)
			} else if len(slsChanged) > 0 {
				fmt.Printf("  [OK] Serverless v3 compat: %v\n", slsChanged)
				filesChanged = append(filesChanged, slsChanged...)
			}

			// Phase 2: LLM-powered API migration for upgraded dependencies
			if llmURL != "" && len(issues) > 0 {
				llmClient := llm.NewClient(llmURL, llmModel)
				if err := llmClient.Ping(); err != nil {
					fmt.Printf("  [LLM-CODEMOD] Cannot reach LLM: %v\n", err)
				} else {
					codemodResult := llm.FixDeprecatedAPIs(llmClient, repoPath, target, issues)
					if len(codemodResult.FilesFixed) > 0 {
						fmt.Printf("  [OK] LLM codemod: migrated %d file(s)\n", len(codemodResult.FilesFixed))
						filesChanged = append(filesChanged, codemodResult.FilesFixed...)
					}
				}
			}

			if len(filesChanged) == 0 {
				fmt.Println("No files needed transformation. Skipping PR.")
				printReport(owner+"/"+repo, configs, issues, filesChanged, target)
				return nil
			}

			// Phase 3: Verification - npm install + tsc + tests
			fmt.Println("\n  [VERIFY] Running npm install...")
			vResult := verify.Verify(repoPath, 2)
			if !vResult.NpmInstallOk {
				fmt.Fprintf(os.Stderr, "  [WARN] npm install failed: %s\n", vResult.NpmErrors)
			} else {
				fmt.Println("  [OK] npm install succeeded")
				filesChanged = append(filesChanged, "package-lock.json")
				if len(vResult.AutoFixed) > 0 {
					fmt.Printf("  [FIX] Auto-fixed: %s\n", vResult.AutoFixed)
					filesChanged = append(filesChanged, vResult.AutoFixed...)
				}
				if vResult.TscOk {
					fmt.Println("  [OK] tsc --noEmit passed (zero errors)")
				} else {
					fmt.Printf("  [WARN] tsc found %d error(s):\n", len(vResult.TscErrors))
					for _, e := range vResult.TscErrors {
						fmt.Printf("    %s(%d,%d): %s %s\n", e.File, e.Line, e.Col, e.Code, e.Message)
					}
				}

				// Always show test results
				if vResult.TestsOk {
					fmt.Println("  [OK] Tests passed")
				} else if len(vResult.TestErrors) > 0 {
					fmt.Printf("  [WARN] %d test(s) failed:\n", len(vResult.TestErrors))
					for _, t := range vResult.TestErrors {
						if t.TestSuite != "" {
							fmt.Printf("    %s > %s\n", t.TestSuite, t.TestName)
						} else {
							fmt.Printf("    %s\n", t.TestName)
						}
						if t.Error != "" {
							fmt.Printf("      %s\n", t.Error)
						}
					}
				}

				// Phase 3b: LLM-assisted fix for tsc errors and test failures
				if llmURL != "" && (!vResult.TscOk || !vResult.TestsOk) {
					llmClient := llm.NewClient(llmURL, llmModel)
					if err := llmClient.Ping(); err != nil {
						fmt.Printf("  [LLM] Cannot reach LLM at %s: %v\n", llmURL, err)
					} else {
						fmt.Printf("  [LLM] Connected to %s (model: %s)\n", llmURL, llmModel)

						if !vResult.TscOk && len(vResult.TscErrors) > 0 {
							fmt.Printf("  [LLM] Fixing %d tsc error(s)...\n", len(vResult.TscErrors))
							tscResult := llm.FixTscErrors(llmClient, repoPath, target)
							filesChanged = append(filesChanged, tscResult.FilesFixed...)
							if len(tscResult.TscRemaining) == 0 {
								fmt.Println("  [OK] All tsc errors fixed by LLM")
								vResult.TscOk = true
								vResult.TscErrors = nil
							} else {
								fmt.Printf("  [WARN] %d tsc error(s) remain after %d LLM attempts\n", len(tscResult.TscRemaining), tscResult.AttemptsMade)
								vResult.TscErrors = tscResult.TscRemaining
							}
						}

						if !vResult.TestsOk && len(vResult.TestErrors) > 0 {
							fmt.Printf("  [LLM] Fixing %d test failure(s)...\n", len(vResult.TestErrors))
							testResult := llm.FixTestErrors(llmClient, repoPath, target)
							filesChanged = append(filesChanged, testResult.FilesFixed...)
							if len(testResult.TestRemaining) == 0 {
								fmt.Println("  [OK] All test failures fixed by LLM")
								vResult.TestsOk = true
								vResult.TestErrors = nil
							} else {
								fmt.Printf("  [WARN] %d test failure(s) remain after %d LLM attempts\n", len(testResult.TestRemaining), testResult.AttemptsMade)
							}
						}
					}
				}
			}

			// Phase 4: Vulnerability scan + fix
			fmt.Println("\n  [AUDIT] Scanning for vulnerabilities...")
			auditResult := verify.RunAudit(repoPath)
			vResult.Audit = auditResult
			beforeCounts := verify.AuditSummary(auditResult.Before)
			beforeTotal := len(auditResult.Before)

			if beforeTotal == 0 {
				fmt.Println("  [OK] No vulnerabilities found")
			} else {
				fmt.Printf("  [WARN] Found %d vulnerabilities:", beforeTotal)
				for _, sev := range []string{"critical", "high", "moderate", "low"} {
					if c, ok := beforeCounts[sev]; ok {
						fmt.Printf(" %d %s", c, sev)
					}
				}
				fmt.Println()

				if auditResult.FixApplied {
					afterTotal := len(auditResult.After)
					fixed := beforeTotal - afterTotal
					if fixed > 0 {
						fmt.Printf("  [FIX] npm audit fix resolved %d/%d vulnerabilities\n", fixed, beforeTotal)
						filesChanged = append(filesChanged, "package-lock.json")
					}
					if afterTotal > 0 {
						afterCounts := verify.AuditSummary(auditResult.After)
						fmt.Printf("  [WARN] %d remaining:", afterTotal)
						for _, sev := range []string{"critical", "high", "moderate", "low"} {
							if c, ok := afterCounts[sev]; ok {
								fmt.Printf(" %d %s", c, sev)
							}
						}
						fmt.Println()
						for _, v := range auditResult.After {
							if v.IsDirect && (v.Severity == "critical" || v.Severity == "high") {
								fmt.Printf("    [%s] %s (direct dep)\n", strings.ToUpper(v.Severity), v.Name)
							}
						}
					} else {
						fmt.Println("  [OK] All vulnerabilities resolved")
					}

					fmt.Println("\n  [VERIFY] Re-checking tsc after audit fix...")
					tscOk2, tscErrors2 := verify.RunTsc(repoPath)
					if tscOk2 {
						fmt.Println("  [OK] tsc --noEmit still passes")
					} else {
						fmt.Printf("  [WARN] tsc found %d new error(s) after audit fix\n", len(tscErrors2))
						for _, e := range tscErrors2 {
							fmt.Printf("    %s(%d,%d): %s %s\n", e.File, e.Line, e.Col, e.Code, e.Message)
						}
					}
				} else if auditResult.FixError != "" {
					fmt.Printf("  [WARN] %s\n", auditResult.FixError)
				}
			}

			// Phase 4b: LLM-assisted vulnerability resolution
			if llmURL != "" && len(vResult.Audit.After) > 0 {
				llmClient := llm.NewClient(llmURL, llmModel)
				if err := llmClient.Ping(); err == nil {
					fmt.Printf("\n  [LLM] Attempting to resolve %d remaining vulnerabilities...\n", len(vResult.Audit.After))
					vulnResult := llm.FixVulnerabilities(llmClient, repoPath, vResult.Audit.After)
					filesChanged = append(filesChanged, vulnResult.FilesFixed...)
					if len(vulnResult.FilesFixed) > 0 {
						fmt.Printf("  [LLM] Modified %d file(s) to resolve vulnerabilities\n", len(vulnResult.FilesFixed))
						// Re-run audit to check remaining
						reAudit := verify.RunAudit(repoPath)
						remaining := len(reAudit.After)
						if remaining == 0 {
							fmt.Println("  [OK] All vulnerabilities resolved by LLM")
						} else {
							fmt.Printf("  [WARN] %d vulnerabilities still remain\n", remaining)
						}
						vResult.Audit.After = reAudit.After
					} else {
						fmt.Println("  [LLM] No vulnerability fixes could be applied")
					}
				}
			}

			// Stamp new version onto detected configs for the PR body
			targetStr := strconv.Itoa(target)
			for i := range configs {
				configs[i].NewVersion = targetStr
			}

			// Phase 5: Commit, push, and create/update PR
			if canPR {
				// Do not push/PR if tests are failing
				if !vResult.TestsOk {
					fmt.Println("\n  [FAIL] Tests failed — not creating/updating PR.")
					printReport(owner+"/"+repo, configs, issues, filesChanged, target)
					return fmt.Errorf("tests failed: upgrade aborted")
				}

				if err := gh.CommitAndPush(repoPath, filesChanged, branch); err != nil {
					return err
				}

				report := types.UpgradeReport{
					Repo:             owner + "/" + repo,
					DetectedConfigs:  configs,
					DependencyIssues: issues,
					FilesChanged:     filesChanged,
				}
				prURL, err := gh.CreatePR(report, branch)
				if err != nil {
					return err
				}

				if prURL != "" {
					fmt.Printf("\nPR: %s\n", prURL)
				}
			} else if dryRun {
				fmt.Println("\n  [DRY RUN] Changes applied locally. No push/PR.")
			}

			printReport(owner+"/"+repo, configs, issues, filesChanged, target)
			return nil
		},
	}

	cmd.Flags().IntVarP(&target, "target", "t", 0, "Target Node.js major version (0 = auto-detect latest LTS)")
	cmd.Flags().StringVarP(&baseBranch, "base", "b", "master", "Base branch for PR")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without pushing")
	cmd.Flags().StringVar(&llmURL, "llm-url", "", "Ollama API base URL (enables LLM fix during upgrade)")
	cmd.Flags().StringVar(&llmModel, "llm-model", "qwen2.5-coder:7b", "LLM model name")

	return cmd
}

func batchCmd() *cobra.Command {
	var (
		target     int
		baseBranch string
		token      string
		dryRun     bool
		reposFile  string
		llmURL     string
		llmModel   string
	)

	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Upgrade multiple repos from a JSON file",
		Long: `Process multiple repositories in sequence. Provide a JSON file with:
[
  {"url": "https://github.com/org/repo1.git", "baseBranch": "develop"},
  {"url": "https://github.com/org/repo2.git"},
  {"owner": "org", "name": "repo3", "baseBranch": "main"}
]

Each repo is cloned, upgraded, and a PR is raised. Results are printed as a summary table.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("GITHUB_TOKEN required for batch mode (set in .env or environment)")
			}

			// Read repo list
			var repos []types.RepoEntry
			data, err := os.ReadFile(reposFile)
			if err != nil {
				return fmt.Errorf("cannot read repos file %s: %w", reposFile, err)
			}
			if err := json.Unmarshal(data, &repos); err != nil {
				return fmt.Errorf("invalid JSON in %s: %w", reposFile, err)
			}

			if len(repos) == 0 {
				return fmt.Errorf("no repos found in %s", reposFile)
			}

			fmt.Printf("\n=== NODESHIFT BATCH: %d repos, target Node %d ===\n\n", len(repos), target)

			var results []types.BatchResult
			for i, entry := range repos {
				// Resolve URL from owner/name if not provided
				repoURL := entry.URL
				if repoURL == "" && entry.Owner != "" && entry.Name != "" {
					repoURL = fmt.Sprintf("https://github.com/%s/%s.git", entry.Owner, entry.Name)
				}
				if repoURL == "" {
					results = append(results, types.BatchResult{
						Repo:   "unknown",
						Status: "error",
						Error:  "no url or owner/name provided",
					})
					continue
				}

				owner, repo := parseGitHubURL(repoURL)
				repoLabel := owner + "/" + repo
				base := baseBranch
				if entry.BaseBranch != "" {
					base = entry.BaseBranch
				}

				fmt.Printf("━━━ [%d/%d] %s (base: %s) ━━━\n", i+1, len(repos), repoLabel, base)
				start := time.Now()

				result := processSingleRepo(token, repoURL, owner, repo, base, target, dryRun, llmURL, llmModel)
				result.Repo = repoLabel

				elapsed := time.Since(start).Round(time.Second)
				switch result.Status {
				case "success":
					fmt.Printf("  ✅ Done in %s → %s\n\n", elapsed, result.PRUrl)
				case "up-to-date":
					fmt.Printf("  ⏭️  Already up-to-date (%s)\n\n", elapsed)
				case "error":
					fmt.Printf("  ❌ Failed in %s: %s\n\n", elapsed, result.Error)
				}

				results = append(results, result)
			}

			// Print summary table
			printBatchSummary(results)

			// Write results JSON
			resultsJSON, _ := json.MarshalIndent(results, "", "  ")
			resultsPath := "/tmp/nodeshift-batch-results.json"
			os.WriteFile(resultsPath, resultsJSON, 0644)
			fmt.Printf("\nResults written to %s\n", resultsPath)

			return nil
		},
	}

	cmd.Flags().StringVarP(&reposFile, "file", "f", "repos.json", "JSON file with repo list")
	cmd.Flags().IntVarP(&target, "target", "t", 24, "Target Node.js major version")
	cmd.Flags().StringVarP(&baseBranch, "base", "b", "master", "Default base branch (overridden per-repo)")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without pushing")
	cmd.Flags().StringVar(&llmURL, "llm-url", "", "Ollama API base URL (enables LLM fix during upgrade)")
	cmd.Flags().StringVar(&llmModel, "llm-model", "qwen2.5-coder:7b", "LLM model name")

	return cmd
}

// processSingleRepo runs the full upgrade pipeline on one repo and returns a result.
func processSingleRepo(token, repoURL, owner, repo, baseBranch string, target int, dryRun bool, llmURL, llmModel string) types.BatchResult {
	gh := ghclient.New(token, owner, repo, baseBranch, target, dryRun, "/tmp/upgrade-work")

	repoPath, err := gh.Clone()
	if err != nil {
		return types.BatchResult{Status: "error", Error: fmt.Sprintf("clone: %v", err)}
	}

	configs, err := detector.Scan(repoPath)
	if err != nil {
		return types.BatchResult{Status: "error", Error: fmt.Sprintf("scan: %v", err)}
	}

	issues, err := analyzer.Analyze(repoPath, target)
	if err != nil {
		return types.BatchResult{Status: "error", Error: fmt.Sprintf("analyze: %v", err)}
	}

	branch, err := gh.CreateBranch(repoPath)
	if err != nil {
		return types.BatchResult{Status: "error", Error: fmt.Sprintf("branch: %v", err)}
	}

	// Phase 1: Config transforms + package.json upgrades
	var filesChanged []string
	configChanged, err := transformer.Transform(repoPath, configs, target, issues)
	if err != nil {
		return types.BatchResult{Status: "error", Error: fmt.Sprintf("transform: %v", err)}
	}
	filesChanged = append(filesChanged, configChanged...)

	// Phase 2: LLM-powered API migration for upgraded dependencies
	if llmURL != "" && len(issues) > 0 {
		llmClient := llm.NewClient(llmURL, llmModel)
		if err := llmClient.Ping(); err != nil {
			fmt.Printf("  [LLM-CODEMOD] Cannot reach LLM: %v\n", err)
		} else {
			codemodResult := llm.FixDeprecatedAPIs(llmClient, repoPath, target, issues)
			if len(codemodResult.FilesFixed) > 0 {
				fmt.Printf("  [OK] LLM codemod: migrated %d file(s)\n", len(codemodResult.FilesFixed))
				filesChanged = append(filesChanged, codemodResult.FilesFixed...)
			}
		}
	}

	if len(filesChanged) == 0 {
		return types.BatchResult{Status: "up-to-date"}
	}

	// Phase 3: Verification
	fmt.Println("  [VERIFY] Running npm install...")
	vResult := verify.Verify(repoPath, 2)
	if !vResult.NpmInstallOk {
		fmt.Fprintf(os.Stderr, "  [WARN] npm install failed: %s\n", vResult.NpmErrors)
	} else {
		fmt.Println("  [OK] npm install succeeded")
		if len(vResult.AutoFixed) > 0 {
			fmt.Printf("  [FIX] Auto-fixed: %s\n", vResult.AutoFixed)
			filesChanged = append(filesChanged, vResult.AutoFixed...)
		}
		if vResult.TscOk {
			fmt.Println("  [OK] tsc passed")
		} else {
			fmt.Printf("  [WARN] tsc: %d error(s)\n", len(vResult.TscErrors))
		}
		if vResult.TestsOk {
			fmt.Println("  [OK] Tests passed")
		} else if len(vResult.TestErrors) > 0 {
			fmt.Printf("  [WARN] %d test(s) failed\n", len(vResult.TestErrors))
		}

		// Phase 3b: LLM-assisted fix for tsc errors and test failures
		if llmURL != "" && (!vResult.TscOk || !vResult.TestsOk) {
			llmClient := llm.NewClient(llmURL, llmModel)
			if err := llmClient.Ping(); err == nil {
				fmt.Printf("  [LLM] Connected to %s (model: %s)\n", llmURL, llmModel)

				if !vResult.TscOk && len(vResult.TscErrors) > 0 {
					fmt.Printf("  [LLM] Fixing %d tsc error(s)...\n", len(vResult.TscErrors))
					tscResult := llm.FixTscErrors(llmClient, repoPath, target)
					filesChanged = append(filesChanged, tscResult.FilesFixed...)
					if len(tscResult.TscRemaining) == 0 {
						fmt.Println("  [OK] All tsc errors fixed by LLM")
						vResult.TscOk = true
					} else {
						fmt.Printf("  [WARN] %d tsc error(s) remain\n", len(tscResult.TscRemaining))
					}
				}

				if !vResult.TestsOk && len(vResult.TestErrors) > 0 {
					fmt.Printf("  [LLM] Fixing %d test failure(s)...\n", len(vResult.TestErrors))
					testResult := llm.FixTestErrors(llmClient, repoPath, target)
					filesChanged = append(filesChanged, testResult.FilesFixed...)
					if len(testResult.TestRemaining) == 0 {
						fmt.Println("  [OK] All test failures fixed by LLM")
						vResult.TestsOk = true
					} else {
						fmt.Printf("  [WARN] %d test failure(s) remain\n", len(testResult.TestRemaining))
					}
				}
			}
		}
	}

	// Phase 4: Audit
	auditResult := verify.RunAudit(repoPath)
	if auditResult.FixApplied && len(auditResult.Before) > len(auditResult.After) {
		filesChanged = append(filesChanged, "package-lock.json")
	}

	// Phase 4b: LLM-assisted vulnerability resolution
	if llmURL != "" && len(auditResult.After) > 0 {
		llmClient := llm.NewClient(llmURL, llmModel)
		if err := llmClient.Ping(); err == nil {
			fmt.Printf("  [LLM] Attempting to resolve %d remaining vulnerabilities...\n", len(auditResult.After))
			vulnResult := llm.FixVulnerabilities(llmClient, repoPath, auditResult.After)
			filesChanged = append(filesChanged, vulnResult.FilesFixed...)
			if len(vulnResult.FilesFixed) > 0 {
				fmt.Printf("  [LLM] Modified %d file(s) to resolve vulnerabilities\n", len(vulnResult.FilesFixed))
			}
		}
	}

	// Stamp new version
	targetStr := strconv.Itoa(target)
	for i := range configs {
		configs[i].NewVersion = targetStr
	}

	// Phase 5: Commit, push, PR
	if dryRun {
		return types.BatchResult{Status: "success", PRUrl: "dry-run://no-pr"}
	}

	if err := gh.CommitAndPush(repoPath, filesChanged, branch); err != nil {
		return types.BatchResult{Status: "error", Error: fmt.Sprintf("push: %v", err)}
	}

	report := types.UpgradeReport{
		Repo:             owner + "/" + repo,
		DetectedConfigs:  configs,
		DependencyIssues: issues,
		FilesChanged:     filesChanged,
	}

	prURL, err := gh.CreatePR(report, branch)
	if err != nil {
		return types.BatchResult{Status: "error", Error: fmt.Sprintf("PR: %v", err)}
	}

	return types.BatchResult{Status: "success", PRUrl: prURL}
}

func printBatchSummary(results []types.BatchResult) {
	fmt.Println("\n╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║                  BATCH SUMMARY                         ║")
	fmt.Println("╠══════════════════════════════════════════════════════════╣")

	success, upToDate, errors := 0, 0, 0
	for _, r := range results {
		var icon, detail string
		switch r.Status {
		case "success":
			icon = "✅"
			detail = r.PRUrl
			success++
		case "up-to-date":
			icon = "⏭️ "
			detail = "already up-to-date"
			upToDate++
		case "error":
			icon = "❌"
			detail = r.Error
			errors++
		}
		fmt.Printf("║ %s %-30s %s\n", icon, r.Repo, detail)
	}

	fmt.Println("╠══════════════════════════════════════════════════════════╣")
	fmt.Printf("║ Total: %d  |  ✅ %d  |  ⏭️  %d  |  ❌ %d\n", len(results), success, upToDate, errors)
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
}

func printReport(repoName string, configs []types.DetectedNodeConfig, issues []types.DependencyIssue, filesChanged []string, target int) {
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Printf("  NODE UPGRADE REPORT: %s\n", repoName)
	fmt.Printf("  Target: Node.js %d\n", target)
	fmt.Println("============================================================")

	fmt.Println("\nDetected Node Configs:")
	for _, cfg := range configs {
		icon := "[OK]"
		v, _ := strconv.Atoi(cfg.CurrentVersion)
		if v < target {
			icon = "[!!]"
		}
		fmt.Printf("  %s %s (%s): Node %s\n", icon, cfg.File, cfg.Type, cfg.CurrentVersion)
	}

	if len(issues) > 0 {
		fmt.Println("\nDependency Issues:")
		for _, issue := range issues {
			icon := "[LOW]"
			switch issue.Severity {
			case "high":
				icon = "[HIGH]"
			case "medium":
				icon = "[MED]"
			}
			fmt.Printf("  %s %s (%s): %s\n", icon, issue.Name, issue.CurrentVersion, issue.Reason)
			if issue.SuggestedVersion != "" {
				fmt.Printf("       -> Suggested: %s\n", issue.SuggestedVersion)
			}
		}
	}

	if len(filesChanged) > 0 {
		fmt.Println("\nFiles Changed:")
		for _, f := range filesChanged {
			fmt.Printf("  - %s\n", f)
		}
	}

	fmt.Println("\n============================================================")
}

// parseGitHubURL extracts owner and repo from a GitHub URL.
// Supports: https://github.com/org/repo.git, https://github.com/org/repo, git@github.com:org/repo.git
func parseGitHubURL(url string) (string, string) {
	// Remove trailing .git
	url = strings.TrimSuffix(url, ".git")

	// HTTPS format: https://github.com/owner/repo or https://x-access-token:xxx@github.com/owner/repo
	if strings.Contains(url, "github.com/") {
		parts := strings.Split(url, "github.com/")
		if len(parts) == 2 {
			segments := strings.Split(parts[1], "/")
			if len(segments) >= 2 {
				return segments[0], segments[1]
			}
		}
	}

	// SSH format: git@github.com:owner/repo
	if strings.HasPrefix(url, "git@github.com:") {
		path := strings.TrimPrefix(url, "git@github.com:")
		segments := strings.Split(path, "/")
		if len(segments) >= 2 {
			return segments[0], segments[1]
		}
	}

	return "", ""
}

// getGitRemoteURL reads the origin remote URL from a local git repo
func getGitRemoteURL(repoPath string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runShell runs a shell command and returns any error
func runShell(command string) error {
	cmd := exec.Command("sh", "-c", command)
	return cmd.Run()
}
