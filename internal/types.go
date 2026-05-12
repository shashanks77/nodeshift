package internal

// DetectedNodeConfig represents a discovered Node.js version configuration.
type DetectedNodeConfig struct {
	File           string `json:"file"`
	Type           string `json:"type"`
	CurrentVersion string `json:"currentVersion"`
	NewVersion     string `json:"newVersion,omitempty"`
	Line           int    `json:"line,omitempty"`
}

// DependencyIssue represents a package that may block the Node upgrade.
type DependencyIssue struct {
	Name             string `json:"name"`
	CurrentVersion   string `json:"currentVersion"`
	Issue            string `json:"issue"`
	Severity         string `json:"severity"`
	Reason           string `json:"reason"`
	SuggestedVersion string `json:"suggestedVersion,omitempty"`
}

// UpgradeReport summarises the upgrade for PR description and logging.
type UpgradeReport struct {
	Repo             string               `json:"repo"`
	DetectedConfigs  []DetectedNodeConfig  `json:"detectedConfigs"`
	DependencyIssues []DependencyIssue     `json:"dependencyIssues"`
	FilesChanged     []string              `json:"filesChanged"`
}

// RepoEntry is one repository to process (Lambda batch mode).
type RepoEntry struct {
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	BaseBranch string `json:"baseBranch,omitempty"`
}
