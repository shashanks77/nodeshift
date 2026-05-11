package verify

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type TscError struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type TestError struct {
	TestSuite string `json:"testSuite"`
	TestName  string `json:"testName"`
	Error     string `json:"error"`
}

type Vulnerability struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	IsDirect bool   `json:"isDirect"`
	Via      string `json:"via"`
}

type AuditResult struct {
	Before     []Vulnerability `json:"before"`
	After      []Vulnerability `json:"after"`
	FixApplied bool            `json:"fixApplied"`
	FixError   string          `json:"fixError,omitempty"`
}

type VerifyResult struct {
	NpmInstallOk bool        `json:"npmInstallOk"`
	NpmErrors    string      `json:"npmErrors,omitempty"`
	TscOk        bool        `json:"tscOk"`
	TscErrors    []TscError  `json:"tscErrors,omitempty"`
	TestsOk      bool        `json:"testsOk"`
	TestErrors   []TestError `json:"testErrors,omitempty"`
	AutoFixed    []string    `json:"autoFixed,omitempty"`
	Audit        AuditResult `json:"audit,omitempty"`
}

func Verify(repoPath string, maxRetries int) VerifyResult {
	result := VerifyResult{}

	ok, errMsg := RunNpmInstall(repoPath)
	if !ok {
		fixed := AutoFixPeerDeps(repoPath)
		if len(fixed) > 0 {
			result.AutoFixed = fixed
			ok, errMsg = RunNpmInstall(repoPath)
		}
	}
	result.NpmInstallOk = ok
	result.NpmErrors = errMsg
	if !ok {
		return result
	}

	result.TscOk, result.TscErrors = RunTsc(repoPath)

	if result.TscOk {
		result.TestsOk, result.TestErrors = RunTests(repoPath)
	}

	return result
}

func RunNpmInstall(repoPath string) (bool, string) {
	out, err := runWithTimeout(repoPath, 600*time.Second, "npm", "install", "--legacy-peer-deps")
	if err != nil {
		return false, fmt.Sprintf("npm install failed: %s\n%s", err, out)
	}
	return true, ""
}

func RunTsc(repoPath string) (bool, []TscError) {
	out, _ := runWithTimeout(repoPath, 60*time.Second, "npx", "tsc", "--noEmit")
	errors := parseTscOutput(out)
	return len(errors) == 0, errors
}

func RunTests(repoPath string) (bool, []TestError) {
	FixTestConfig(repoPath)

	env := os.Environ()
	env = append(env,
		"NODE_OPTIONS=--experimental-vm-modules",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION=us-east-1",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "npx", "jest", "--no-coverage", "--forceExit", "--passWithNoTests")
	cmd.Dir = repoPath
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	outBytes, err := cmd.CombinedOutput()
	out := string(outBytes)

	if ctx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return false, []TestError{{TestName: "timeout", Error: "Tests timed out after 120s"}}
	}

	if err != nil {
		errors := parseTestOutput(out)
		if len(errors) == 0 {
			errors = []TestError{{TestName: "jest", Error: out}}
		}
		return false, errors
	}
	return true, nil
}

func FixTestConfig(repoPath string) {
	pkgPath := filepath.Join(repoPath, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return
	}

	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}

	jest, ok := pkg["jest"].(map[string]interface{})
	if !ok {
		return
	}

	if _, exists := jest["moduleDirectories"]; !exists {
		jest["moduleDirectories"] = []string{"node_modules", "<rootDir>"}
		pkg["jest"] = jest

		out, err := json.MarshalIndent(pkg, "", "  ")
		if err != nil {
			return
		}
		os.WriteFile(pkgPath, append(out, '\n'), 0644)
	}
}

func AutoFixPeerDeps(repoPath string) []string {
	_, err := runWithTimeout(repoPath, 600*time.Second, "npm", "install", "--legacy-peer-deps")
	if err != nil {
		return nil
	}
	return []string{"--legacy-peer-deps"}
}

func RunAudit(repoPath string) AuditResult {
	result := AuditResult{}

	beforeOut, _ := runWithTimeout(repoPath, 60*time.Second, "npm", "audit", "--json")
	result.Before = parseNpmAudit(beforeOut)

	if len(result.Before) == 0 {
		return result
	}

	_, fixErr := runWithTimeout(repoPath, 120*time.Second, "npm", "audit", "fix", "--legacy-peer-deps")
	if fixErr != nil && strings.Contains(fixErr.Error(), "timed out") {
		result.FixError = "npm audit fix timed out"
		return result
	}
	result.FixApplied = true

	afterOut, _ := runWithTimeout(repoPath, 60*time.Second, "npm", "audit", "--json")
	result.After = parseNpmAudit(afterOut)

	return result
}

func AuditSummary(vulns []Vulnerability) map[string]int {
	counts := make(map[string]int)
	for _, v := range vulns {
		counts[v.Severity]++
	}
	return counts
}

func parseNpmAudit(output string) []Vulnerability {
	var vulns []Vulnerability
	if output == "" {
		return vulns
	}

	var audit struct {
		Vulnerabilities map[string]struct {
			Name     string        `json:"name"`
			Severity string        `json:"severity"`
			IsDirect bool          `json:"isDirect"`
			Via      []interface{} `json:"via"`
			Effects  []string      `json:"effects"`
		} `json:"vulnerabilities"`
	}

	if err := json.Unmarshal([]byte(output), &audit); err != nil {
		return vulns
	}

	for name, v := range audit.Vulnerabilities {
		title := ""
		url := ""
		for _, via := range v.Via {
			switch vt := via.(type) {
			case map[string]interface{}:
				if t, ok := vt["title"].(string); ok {
					title = t
				}
				if u, ok := vt["url"].(string); ok {
					url = u
				}
			}
		}

		vulns = append(vulns, Vulnerability{
			Name:     name,
			Severity: v.Severity,
			Title:    title,
			URL:      url,
			IsDirect: v.IsDirect,
		})
	}

	return vulns
}

func parseTscOutput(output string) []TscError {
	var errors []TscError
	re := regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):\s+(error\s+TS\d+):\s+(.+)$`)

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		m := re.FindStringSubmatch(line)
		if m != nil {
			lineNum, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			errors = append(errors, TscError{
				File:    m[1],
				Line:    lineNum,
				Col:     col,
				Code:    m[4],
				Message: m[5],
			})
		}
	}
	return errors
}

func parseTestOutput(output string) []TestError {
	var errors []TestError
	re := regexp.MustCompile(`(?m)^\s*●\s+(.+?)\s*›\s*(.+)$`)
	matches := re.FindAllStringSubmatch(output, -1)
	for _, m := range matches {
		errors = append(errors, TestError{
			TestSuite: m[1],
			TestName:  m[2],
		})
	}
	return errors
}

func runWithTimeout(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	outBytes, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return string(outBytes), fmt.Errorf("%s timed out after %v", name, timeout)
	}

	return string(outBytes), err
}
