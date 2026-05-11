package codemod

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Engine struct {
	Dir string
}

type Request struct {
	RepoPath      string   `json:"repoPath"`
	TargetVersion int      `json:"targetVersion"`
	Codemods      []string `json:"codemods,omitempty"`
}

type Response struct {
	Results            []CodemodResult `json:"results"`
	TotalFilesModified []string        `json:"totalFilesModified"`
}

type CodemodResult struct {
	Name          string   `json:"name"`
	Success       bool     `json:"success"`
	FilesModified []string `json:"filesModified"`
	Error         string   `json:"error,omitempty"`
}

func NewEngine(dir string) *Engine {
	return &Engine{Dir: dir}
}

func (e *Engine) Run(repoPath string, targetVersion int, codemods []string) (*Response, error) {
	// Resolve engine directory to absolute path
	engineDir, err := resolveEngineDir(e.Dir)
	if err != nil {
		return nil, fmt.Errorf("resolve engine dir: %w", err)
	}

	// Resolve repo path to absolute
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}

	// Ensure node_modules exist
	nodeModules := filepath.Join(engineDir, "node_modules")
	if _, err := os.Stat(nodeModules); os.IsNotExist(err) {
		fmt.Println("  [CODEMOD] Installing codemod engine dependencies...")
		cmd := exec.Command("npm", "install")
		cmd.Dir = engineDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("npm install in codemod-engine: %w", err)
		}
	}

	req := Request{
		RepoPath:      absRepoPath,
		TargetVersion: targetVersion,
		Codemods:      codemods,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	cmd := exec.Command("npx", "ts-node", "--transpile-only", "src/index.ts")
	cmd.Dir = engineDir
	cmd.Stdin = strings.NewReader(string(reqJSON))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("codemod engine failed: %w (stderr: %s)", err, stderr.String())
		}
	case <-time.After(120 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil, fmt.Errorf("codemod engine timed out after 120s")
	}

	outBytes := stdout.Bytes()
	var resp Response
	if err := json.Unmarshal(outBytes, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w\nRaw: %s", err, string(outBytes))
	}

	return &resp, nil
}

func resolveEngineDir(dir string) (string, error) {
	// If already absolute, use directly
	if filepath.IsAbs(dir) {
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return dir, nil
		}
		return "", fmt.Errorf("engine dir %s does not contain package.json", dir)
	}

	// Try relative to executable location
	exePath, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exePath), "..", dir)
		abs, err := filepath.Abs(candidate)
		if err == nil {
			if _, err := os.Stat(filepath.Join(abs, "package.json")); err == nil {
				return abs, nil
			}
		}
	}

	// Try relative to current working directory
	cwd, err := os.Getwd()
	if err == nil {
		candidate := filepath.Join(cwd, dir)
		if _, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs, nil
		}
	}

	return "", fmt.Errorf("cannot find codemod engine at %s", dir)
}
