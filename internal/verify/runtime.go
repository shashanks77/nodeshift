package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// RuntimeResult holds the result of the runtime verification.
type RuntimeResult struct {
	Started    bool   `json:"started"`
	Healthy    bool   `json:"healthy"`
	Error      string `json:"error,omitempty"`
	StartCmd   string `json:"startCmd"`
	Port       int    `json:"port"`
	StatusCode int    `json:"statusCode,omitempty"`
}

// RunRuntimeCheck starts the application and verifies it responds to HTTP requests.
// It tries to detect the start command and port from package.json, then:
// 1. Starts the app
// 2. Waits for the port to become available (up to 30s)
// 3. Makes a GET request to health/root endpoint
// 4. Kills the process
func RunRuntimeCheck(repoPath string) RuntimeResult {
	result := RuntimeResult{}

	startCmd, port := detectStartConfig(repoPath)
	if startCmd == "" {
		result.Error = "no start command found in package.json"
		return result
	}

	result.StartCmd = startCmd
	result.Port = port

	// Build first if there's a build script and dist/ doesn't exist
	if needsBuild(repoPath) {
		fmt.Println("  [RUNTIME] Building application...")
		buildOut, err := runWithTimeout(repoPath, 120*time.Second, "npm", "run", "build")
		if err != nil {
			result.Error = fmt.Sprintf("build failed: %s\n%s", err, buildOut)
			return result
		}
	}

	// Start the application
	fmt.Printf("  [RUNTIME] Starting app (%s) on port %d...\n", startCmd, port)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	parts := strings.Fields(startCmd)
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"NODE_ENV=production",
	)
	// Don't capture stdout/stderr to avoid blocking
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		result.Error = fmt.Sprintf("failed to start: %v", err)
		return result
	}
	result.Started = true

	// Ensure cleanup
	defer func() {
		if cmd.Process != nil {
			// Kill the process group
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			cmd.Wait()
		}
	}()

	// Wait for port to become available
	if !waitForPort(port, 30*time.Second) {
		result.Error = fmt.Sprintf("app did not start listening on port %d within 30s", port)
		return result
	}

	// Try health check endpoints
	endpoints := []string{
		fmt.Sprintf("http://localhost:%d/health", port),
		fmt.Sprintf("http://localhost:%d/api/health", port),
		fmt.Sprintf("http://localhost:%d/", port),
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, url := range endpoints {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		resp.Body.Close()
		result.StatusCode = resp.StatusCode
		if resp.StatusCode >= 200 && resp.StatusCode < 500 {
			result.Healthy = true
			fmt.Printf("  [OK] App responded with %d on %s\n", resp.StatusCode, url)
			return result
		}
	}

	if result.StatusCode == 0 {
		result.Error = "app started but no health endpoint responded"
	} else {
		result.Error = fmt.Sprintf("app responded with status %d", result.StatusCode)
	}
	return result
}

// detectStartConfig reads package.json to determine the start command and port.
func detectStartConfig(repoPath string) (string, int) {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return "", 0
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
		Main    string            `json:"main"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", 0
	}

	// Determine start command
	var startCmd string
	if cmd, ok := pkg.Scripts["start:prod"]; ok {
		startCmd = cmd
	} else if cmd, ok := pkg.Scripts["start"]; ok {
		startCmd = cmd
	} else if pkg.Main != "" {
		startCmd = "node " + pkg.Main
	}

	if startCmd == "" {
		return "", 0
	}

	// Determine port — look for PORT in scripts, env files, or default
	port := detectPort(repoPath, data)

	return startCmd, port
}

// detectPort tries to find the port the application listens on.
func detectPort(repoPath string, pkgData []byte) int {
	// Check for common config patterns
	configFiles := []string{
		"src/config.ts", "src/config.js",
		"config/default.json", ".env",
		"src/main.ts", "src/main.js",
	}

	for _, f := range configFiles {
		data, err := os.ReadFile(filepath.Join(repoPath, f))
		if err != nil {
			continue
		}
		content := string(data)
		// Look for port assignments like: port: 3000, PORT = 3000, listen(3000)
		for _, port := range []int{3000, 8080, 4000, 5000, 9000, 8000} {
			portStr := fmt.Sprintf("%d", port)
			if strings.Contains(content, portStr) {
				return port
			}
		}
	}

	// Default to 3000
	return 3000
}

// needsBuild checks if the app needs to be built before running.
func needsBuild(repoPath string) bool {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return false
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	json.Unmarshal(data, &pkg)

	// If start:prod uses dist/ and dist/ doesn't exist, need to build
	startProd := pkg.Scripts["start:prod"]
	if strings.Contains(startProd, "dist/") {
		if _, err := os.Stat(filepath.Join(repoPath, "dist")); os.IsNotExist(err) {
			if _, ok := pkg.Scripts["build"]; ok {
				return true
			}
		}
	}

	return false
}

// waitForPort waits until a TCP connection can be made to localhost:port.
func waitForPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("localhost:%d", port)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
