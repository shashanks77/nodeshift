package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"

	types "github.com/your-org/nodeshift/internal"
)

type Client struct {
	Token         string
	Owner         string
	Repo          string
	BaseBranch    string
	TargetVersion int
	DryRun        bool
	WorkDir       string
	client        *gh.Client
}

func New(token, owner, repo, baseBranch string, targetVersion int, dryRun bool, workDir string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)

	return &Client{
		Token:         token,
		Owner:         owner,
		Repo:          repo,
		BaseBranch:    baseBranch,
		TargetVersion: targetVersion,
		DryRun:        dryRun,
		WorkDir:       workDir,
		client:        gh.NewClient(tc),
	}
}

func (c *Client) Clone() (string, error) {
	cloneURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", c.Token, c.Owner, c.Repo)
	localPath := filepath.Join(c.WorkDir, c.Repo)

	// Remove any previous clone
	os.RemoveAll(localPath)

	cmd := exec.Command("git", "clone", "--branch", c.BaseBranch, cloneURL, localPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	return localPath, nil
}

// BranchName returns the deterministic upgrade branch name.
func (c *Client) BranchName() string {
	return "chore/node-" + strconv.Itoa(c.TargetVersion) + "-upgrade"
}

// SetupBranch checks if the upgrade branch exists remotely.
// If it does, checks it out (to build incrementally on previous work).
// If not, creates a new branch from the current HEAD (base branch).
// Returns (branchName, isExisting, error).
func (c *Client) SetupBranch(repoPath string) (string, bool, error) {
	branch := c.BranchName()

	// Check if branch exists on remote
	cmd := exec.Command("git", "ls-remote", "--heads", "origin", branch)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		// ls-remote failed — assume branch doesn't exist
		return c.createNewBranch(repoPath, branch)
	}

	if strings.TrimSpace(string(out)) != "" {
		// Branch exists remotely — fetch and checkout
		cmd = exec.Command("git", "fetch", "origin", branch)
		cmd.Dir = repoPath
		if err := cmd.Run(); err != nil {
			// Fetch failed — fall back to creating new branch
			return c.createNewBranch(repoPath, branch)
		}

		cmd = exec.Command("git", "checkout", branch)
		cmd.Dir = repoPath
		if err := cmd.Run(); err != nil {
			return "", false, fmt.Errorf("git checkout %s: %w", branch, err)
		}

		fmt.Printf("  [BRANCH] Checked out existing branch: %s\n", branch)
		return branch, true, nil
	}

	return c.createNewBranch(repoPath, branch)
}

func (c *Client) createNewBranch(repoPath, branch string) (string, bool, error) {
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("git checkout -b %s: %w", branch, err)
	}
	fmt.Printf("  [BRANCH] Created new branch: %s\n", branch)
	return branch, false, nil
}

// CreateBranch is kept for backward compatibility but now uses SetupBranch internally.
func (c *Client) CreateBranch(repoPath string) (string, error) {
	branch, _, err := c.SetupBranch(repoPath)
	return branch, err
}

func (c *Client) CommitAndPush(repoPath string, files []string, branch string) error {
	if c.DryRun {
		fmt.Println("[DRY RUN] Would commit and push changes")
		return nil
	}

	// Stage all changed files
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Check if there are any staged changes
	cmd = exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = repoPath
	if err := cmd.Run(); err == nil {
		// Exit code 0 means no diff — nothing to commit
		fmt.Println("  [INFO] No changes to commit — branch already up-to-date")
		return nil
	}

	msg := fmt.Sprintf("chore: upgrade Node.js runtime to %d\n\nAutomated upgrade by nodeshift.", c.TargetVersion)
	cmd = exec.Command("git", "commit", "-m", msg)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	cmd = exec.Command("git", "push", "--force", "origin", branch)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (c *Client) CreatePR(report types.UpgradeReport, branch string) (string, error) {
	if c.DryRun {
		fmt.Println("[DRY RUN] Would create PR")
		return "dry-run://no-pr", nil
	}

	body := c.generatePRBody(report)
	title := fmt.Sprintf("chore: Upgrade Node.js to %d", c.TargetVersion)

	// Check if an open PR already exists for this branch
	existingPR := c.findExistingPR(branch)
	if existingPR != nil {
		// Update the existing PR body
		existingPR.Body = &body
		existingPR.Title = &title
		updated, _, err := c.client.PullRequests.Edit(context.Background(), c.Owner, c.Repo, existingPR.GetNumber(), existingPR)
		if err != nil {
			return "", fmt.Errorf("update PR: %w", err)
		}
		fmt.Printf("  [INFO] Updated existing PR #%d\n", updated.GetNumber())
		return updated.GetHTMLURL(), nil
	}

	pr, _, err := c.client.PullRequests.Create(context.Background(), c.Owner, c.Repo, &gh.NewPullRequest{
		Title: &title,
		Head:  &branch,
		Base:  &c.BaseBranch,
		Body:  &body,
	})
	if err != nil {
		return "", fmt.Errorf("create PR: %w", err)
	}
	return pr.GetHTMLURL(), nil
}

// findExistingPR looks for an open PR from the given branch
func (c *Client) findExistingPR(branch string) *gh.PullRequest {
	prs, _, err := c.client.PullRequests.List(context.Background(), c.Owner, c.Repo, &gh.PullRequestListOptions{
		Head:  c.Owner + ":" + branch,
		State: "open",
	})
	if err != nil || len(prs) == 0 {
		return nil
	}
	return prs[0]
}

func (c *Client) generatePRBody(report types.UpgradeReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Node.js Upgrade to %d\n\n", c.TargetVersion)

	// Verification summary
	if v := report.Verify; v != nil {
		b.WriteString("### Verification Results\n\n")
		b.WriteString("| Phase | Result |\n|-------|--------|\n")

		if v.NpmInstallOk {
			b.WriteString("| npm install | ✅ Passed |\n")
		} else {
			b.WriteString("| npm install | ❌ Failed |\n")
		}

		if v.TscOk {
			b.WriteString("| TypeScript compile | ✅ Passed |\n")
		} else if v.TscFixedByLLM {
			fmt.Fprintf(&b, "| TypeScript compile | ✅ %d error(s) fixed by LLM |\n", v.TscErrorCount)
		} else {
			fmt.Fprintf(&b, "| TypeScript compile | ⚠️ %d error(s) |\n", v.TscErrorCount)
		}

		if v.TestsOk {
			b.WriteString("| Tests | ✅ Passed |\n")
		} else {
			b.WriteString("| Tests | ❌ Failed |\n")
		}

		if v.RuntimeSkipped {
			fmt.Fprintf(&b, "| Runtime check | ⏭️ Skipped — %s |\n", v.RuntimeError)
		} else if v.RuntimeOk {
			b.WriteString("| Runtime check | ✅ App starts and responds |\n")
		} else {
			fmt.Fprintf(&b, "| Runtime check | ⚠️ %s |\n", v.RuntimeError)
		}

		if v.AuditBefore == 0 {
			b.WriteString("| Security audit | ✅ No vulnerabilities |\n")
		} else if v.AuditAfter == 0 {
			fmt.Fprintf(&b, "| Security audit | ✅ %d vulnerabilities fixed |\n", v.AuditBefore)
		} else if v.AuditFixApplied {
			fmt.Fprintf(&b, "| Security audit | ⚠️ %d/%d resolved (npm audit fix) |\n", v.AuditBefore-v.AuditAfter, v.AuditBefore)
		} else {
			fmt.Fprintf(&b, "| Security audit | ⚠️ %d vulnerabilities found |\n", v.AuditBefore)
		}

		b.WriteString("\n")
	}

	b.WriteString("### What changed\n\n")
	b.WriteString("| File | Type | Old Version | New Version |\n|------|------|-------------|-------------|\n")

	for _, cfg := range report.DetectedConfigs {
		newVer := cfg.NewVersion
		if newVer == "" {
			newVer = fmt.Sprintf("%d", c.TargetVersion)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", cfg.File, cfg.Type, cfg.CurrentVersion, newVer)
	}

	if len(report.DependencyIssues) > 0 {
		// Build a set of packages fixed by transformer
		fixedInfo := buildFixedSet()

		b.WriteString("\n### Dependency Issues\n\n| Package | Severity | Issue | Action Taken | Status |\n|---------|----------|-------|--------------|--------|\n")
		for _, issue := range report.DependencyIssues {
			info, isFixed := fixedInfo[issue.Name]
			if isFixed {
				fmt.Fprintf(&b, "| `%s` (%s) | %s | %s | %s | ✅ Fixed |\n", issue.Name, issue.CurrentVersion, issue.Severity, issue.Reason, info)
			} else {
				suggested := issue.SuggestedVersion
				if suggested == "" {
					suggested = "\u2014"
				}
				fmt.Fprintf(&b, "| `%s` (%s) | %s | %s | %s | ⚠️ Manual |\n", issue.Name, issue.CurrentVersion, issue.Severity, issue.Reason, suggested)
			}
		}
	}

	b.WriteString("\n---\n*Automated by nodeshift*\n")
	return b.String()
}

// buildFixedSet returns a map of package name → description of what was done.
func buildFixedSet() map[string]string {
	// Packages upgraded by the Go transformer (version bumps in package.json)
	return map[string]string{
		"typescript":  "Upgraded to `^5.4.0`",
		"jest":        "Upgraded to `^29.7.0`",
		"@types/node": "Upgraded to match target Node version",
		"@types/jest": "Upgraded to `^29.5.0`",
		"ts-jest":     "Upgraded to `^29.1.0`",
	}
}
