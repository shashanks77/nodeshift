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
	DetectedConfigs  []DetectedNodeConfig `json:"detectedConfigs"`
	DependencyIssues []DependencyIssue    `json:"dependencyIssues"`
	FilesChanged     []string             `json:"filesChanged"`
}

// RepoEntry is one repository to process (batch mode).
type RepoEntry struct {
	Owner      string `json:"owner,omitempty"`
	Name       string `json:"name,omitempty"`
	URL        string `json:"url,omitempty"`
	BaseBranch string `json:"baseBranch,omitempty"`
}

// BatchResult holds the outcome of processing one repo in batch mode.
type BatchResult struct {
	Repo   string `json:"repo"`
	Status string `json:"status"` // "success", "up-to-date", "error"
	PRUrl  string `json:"prUrl,omitempty"`
	Error  string `json:"error,omitempty"`
}
