package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

	cmd := exec.Command("git", "clone", "--depth=1", "--branch", c.BaseBranch, cloneURL, localPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	return localPath, nil
}

func (c *Client) CreateBranch(repoPath string) (string, error) {
	date := time.Now().Format("2006-01-02")
	branch := "chore/node-" + strconv.Itoa(c.TargetVersion) + "-upgrade-" + date

	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git checkout -b: %w", err)
	}
	return branch, nil
}

func (c *Client) CommitAndPush(repoPath string, files []string, branch string) error {
	if c.DryRun {
		fmt.Println("[DRY RUN] Would commit and push changes")
		return nil
	}

	args := append([]string{"add"}, files...)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
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
