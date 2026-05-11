package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"

	types "github.com/your-org/nodeshift/internal"
	"github.com/your-org/nodeshift/internal/analyzer"
	"github.com/your-org/nodeshift/internal/detector"
	"github.com/your-org/nodeshift/internal/github"
	"github.com/your-org/nodeshift/internal/transformer"
)

type Result struct {
	Repo   string `json:"repo"`
	Status string `json:"status"`
	PRUrl  string `json:"prUrl,omitempty"`
	Error  string `json:"error,omitempty"`
}

func handler(ctx context.Context) ([]Result, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN not set")
	}

	target, _ := strconv.Atoi(os.Getenv("TARGET_NODE_VERSION"))
	if target == 0 {
		target = 24
	}

	baseBranch := os.Getenv("BASE_BRANCH")
	if baseBranch == "" {
		baseBranch = "main"
	}

	dryRun := os.Getenv("DRY_RUN") == "true"
	repos := getRepos()

	var results []Result
	for _, repo := range repos {
		result := processRepo(token, repo, baseBranch, target, dryRun)
		results = append(results, result)
	}

	return results, nil
}

func processRepo(token string, repo types.RepoEntry, baseBranch string, target int, dryRun bool) Result {
	branch := baseBranch
	if repo.BaseBranch != "" {
		branch = repo.BaseBranch
	}

	gh := github.New(token, repo.Owner, repo.Name, branch, target, dryRun, "/tmp")

	repoPath, err := gh.Clone()
	if err != nil {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "error", Error: err.Error()}
	}
	defer os.RemoveAll(repoPath)

	configs, err := detector.Scan(repoPath)
	if err != nil {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "error", Error: err.Error()}
	}

	issues, err := analyzer.Analyze(repoPath, target)
	if err != nil {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "error", Error: err.Error()}
	}

	branchName, err := gh.CreateBranch(repoPath)
	if err != nil {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "error", Error: err.Error()}
	}

	filesChanged, err := transformer.Transform(repoPath, configs, target, issues)
	if err != nil {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "error", Error: err.Error()}
	}

	if len(filesChanged) == 0 {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "up-to-date"}
	}

	if err := gh.CommitAndPush(repoPath, filesChanged, branchName); err != nil {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "error", Error: err.Error()}
	}

	report := types.UpgradeReport{
		Repo:             repo.Owner + "/" + repo.Name,
		DetectedConfigs:  configs,
		DependencyIssues: issues,
		FilesChanged:     filesChanged,
	}

	prURL, err := gh.CreatePR(report, branchName)
	if err != nil {
		return Result{Repo: repo.Owner + "/" + repo.Name, Status: "error", Error: err.Error()}
	}

	return Result{Repo: repo.Owner + "/" + repo.Name, Status: "pr-created", PRUrl: prURL}
}

func getRepos() []types.RepoEntry {
	// Option 1: Full JSON
	if reposJSON := os.Getenv("REPOS"); reposJSON != "" {
		var repos []types.RepoEntry
		if err := json.Unmarshal([]byte(reposJSON), &repos); err == nil {
			return repos
		}
	}

	// Option 2: GITHUB_OWNER + GITHUB_REPOS
	owner := os.Getenv("GITHUB_OWNER")
	repoNames := os.Getenv("GITHUB_REPOS")
	if owner != "" && repoNames != "" {
		var repos []types.RepoEntry
		for _, name := range strings.Split(repoNames, ",") {
			repos = append(repos, types.RepoEntry{
				Owner: owner,
				Name:  strings.TrimSpace(name),
			})
		}
		return repos
	}

	return nil
}

func main() {
	lambda.Start(handler)
}
