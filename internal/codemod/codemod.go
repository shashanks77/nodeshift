package codemod

import (
"encoding/json"
"fmt"
"os"
"os/exec"
"path/filepath"
"strings"
"time"
)

// Engine manages communication with the TypeScript codemod engine.
type Engine struct {
Dir string // path to codemod-engine directory
}

// Request is sent to the codemod engine via stdin.
type Request struct {
RepoPath      string   `json:"repoPath"`
TargetVersion int      `json:"targetVersion"`
Codemods      []string `json:"codemods,omitempty"`
}

// Response is received from the codemod engine via stdout.
type Response struct {
Results            []CodemodResult `json:"results"`
TotalFilesModified []string        `json:"totalFilesModified"`
}

// CodemodResult is the result of a single codemod.
type CodemodResult struct {
Name          string   `json:"name"`
Success       bool     `json:"success"`
FilesModified []string `json:"filesModified"`
Error         string   `json:"error,omitempty"`
}

// NewEngine creates a new codemod engine pointing at the given directory.
func NewEngine(dir string) *Engine {
return &Engine{Dir: dir}
}

// Run executes the codemod engine against a repo.
func (e *Engine) Run(repoPath string, targetVersion int, codemods []string) (*Response, error) {
engineDir := e.Dir
if !filepath.IsAbs(engineDir) {
exePath, _ := os.Executable()
candidate := filepath.Join(filepath.Dir(exePath), "..", e.Dir)
if _, err := os.Stat(filepath.Join(candidate, "package.json")); err == nil {
engineDir = candidate
}
}

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
RepoPath:      repoPath,
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
cmd.Stderr = os.Stderr

done := make(chan error, 1)
var outBytes []byte

go func() {
var gErr error
outBytes, gErr = cmd.Output()
done <- gErr
}()

select {
case err := <-done:
if err != nil {
return nil, fmt.Errorf("codemod engine failed: %w\nOutput: %s", err, string(outBytes))
}
case <-time.After(120 * time.Second):
if cmd.Process != nil {
cmd.Process.Kill()
}
return nil, fmt.Errorf("codemod engine timed out after 120s")
}

var resp Response
if err := json.Unmarshal(outBytes, &resp); err != nil {
return nil, fmt.Errorf("unmarshal response: %w\nRaw: %s", err, string(outBytes))
}

return &resp, nil
}
