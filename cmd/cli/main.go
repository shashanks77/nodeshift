package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	types "github.com/your-org/nodeshift/internal"
	"github.com/your-org/nodeshift/internal/analyzer"
	"github.com/your-org/nodeshift/internal/codemod"
	"github.com/your-org/nodeshift/internal/detector"
	ghclient "github.com/your-org/nodeshift/internal/github"
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
		local      string
		owner      string
		repo       string
		target     int
		baseBranch string
		token      string
		dryRun     bool
		codemods   bool
		engineDir  string
	)

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade Node version in a repo and raise a PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" && local == "" {
				return fmt.Errorf("GITHUB_TOKEN required for remote repos")
			}

			gh := ghclient.New(token, owner, repo, baseBranch, target, dryRun, "/tmp/upgrade-work")

			repoPath := local
			if repoPath == "" {
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

			localOnly := (local != "" && (owner == "" || repo == ""))

			var branch string
			if !localOnly {
				branch, err = gh.CreateBranch(repoPath)
				if err != nil {
					return err
				}
			}

			// Phase 1: AST codemods (TypeScript engine for code-level changes)
			// Must run BEFORE package.json changes so shouldRun() still sees aws-sdk
			var filesChanged []string
			if codemods {
				engine := codemod.NewEngine(engineDir)
				resp, cErr := engine.Run(repoPath, target, nil)
				if cErr != nil {
					fmt.Fprintf(os.Stderr, "Codemod engine error: %v\n", cErr)
				} else {
					for _, r := range resp.Results {
						if r.Success {
							fmt.Printf("  [OK] Codemod %s: %d files modified\n", r.Name, len(r.FilesModified))
						} else {
							fmt.Printf("  [FAIL] Codemod %s: %s\n", r.Name, r.Error)
						}
					}
					filesChanged = append(filesChanged, resp.TotalFilesModified...)
				}
			}

			// Phase 2: Config transforms + package.json upgrades (Go)
			configChanged, err := transformer.Transform(repoPath, configs, target, issues)
			if err != nil {
				return err
			}
			filesChanged = append(filesChanged, configChanged...)

			if len(filesChanged) == 0 {
				fmt.Println("No files needed transformation. Skipping PR.")
				printReport(owner+"/"+repo, configs, issues, filesChanged, target)
				return nil
			}

			// Phase 3: Verification - npm install + tsc + tests
			if !dryRun {
				fmt.Println("\n  [VERIFY] Running npm install...")
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
							// Show remaining direct dep vulns (actionable)
							for _, v := range auditResult.After {
								if v.IsDirect && (v.Severity == "critical" || v.Severity == "high") {
									fmt.Printf("    [%s] %s (direct dep)\n", strings.ToUpper(v.Severity), v.Name)
								}
							}
						} else {
							fmt.Println("  [OK] All vulnerabilities resolved")
						}

						// Re-verify tsc + tests after audit fix changed deps
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
			}

			if !localOnly {
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
			}

			printReport(owner+"/"+repo, configs, issues, filesChanged, target)
			return nil
		},
	}

	cmd.Flags().StringVarP(&local, "local", "l", "", "Work on local directory")
	cmd.Flags().StringVarP(&owner, "owner", "o", "", "GitHub owner/org")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "GitHub repo name")
	cmd.Flags().IntVarP(&target, "target", "t", 24, "Target Node.js major version")
	cmd.Flags().StringVarP(&baseBranch, "base", "b", "main", "Base branch for PR")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview without pushing")
	cmd.Flags().BoolVar(&codemods, "codemods", false, "Run AST codemods (requires Node.js)")
	cmd.Flags().StringVar(&engineDir, "engine-dir", "./codemod-engine", "Path to the codemod engine")

	return cmd
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
