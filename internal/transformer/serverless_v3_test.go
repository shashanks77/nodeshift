package transformer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlattenServiceProperty(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "object notation to flat",
			input: `service:
  name: my-service

provider:
  runtime: nodejs20.x`,
			expected: `service: my-service

provider:
  runtime: nodejs20.x`,
		},
		{
			name: "already flat - no change",
			input: `service: my-service

provider:
  runtime: nodejs20.x`,
			expected: `service: my-service

provider:
  runtime: nodejs20.x`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := flattenServiceProperty(tt.input)
			if result != tt.expected {
				t.Errorf("flattenServiceProperty()\ngot:\n%s\nwant:\n%s", result, tt.expected)
			}
		})
	}
}

func TestCapServerlessRuntime(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		targetVersion int
		expected      string
	}{
		{
			name:          "cap nodejs24 to nodejs20",
			input:         "  runtime: nodejs24.x",
			targetVersion: 24,
			expected:      "  runtime: nodejs20.x",
		},
		{
			name:          "cap nodejs22 to nodejs20",
			input:         "  runtime: nodejs22.x",
			targetVersion: 22,
			expected:      "  runtime: nodejs20.x",
		},
		{
			name:          "no cap needed for nodejs20",
			input:         "  runtime: nodejs20.x",
			targetVersion: 20,
			expected:      "  runtime: nodejs20.x",
		},
		{
			name:          "no cap needed for nodejs18",
			input:         "  runtime: nodejs18.x",
			targetVersion: 18,
			expected:      "  runtime: nodejs18.x",
		},
		{
			name:          "multiple runtimes capped",
			input:         "  runtime: nodejs24.x\nfunctions:\n  fn1:\n    runtime: nodejs24.x",
			targetVersion: 24,
			expected:      "  runtime: nodejs20.x\nfunctions:\n  fn1:\n    runtime: nodejs20.x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := capServerlessRuntime(tt.input, tt.targetVersion)
			if result != tt.expected {
				t.Errorf("capServerlessRuntime()\ngot:\n%s\nwant:\n%s", result, tt.expected)
			}
		})
	}
}

func TestRemoveDeprecatedPlugin(t *testing.T) {
	input := `plugins:
  - serverless-step-functions
  - serverless-pseudo-parameters
  - serverless-offline`

	expected := `plugins:
  - serverless-step-functions
  - serverless-offline`

	result := removeDeprecatedPlugin(input, "serverless-pseudo-parameters")
	if result != expected {
		t.Errorf("removeDeprecatedPlugin()\ngot:\n%s\nwant:\n%s", result, expected)
	}
}

func TestRemoveDeprecatedPlugin_NotPresent(t *testing.T) {
	input := `plugins:
  - serverless-step-functions
  - serverless-offline`

	result := removeDeprecatedPlugin(input, "serverless-pseudo-parameters")
	if result != input {
		t.Errorf("should not change content when plugin is not present")
	}
}

func TestMoveResourcePolicyToApiGateway(t *testing.T) {
	input := `provider:
  name: aws
  runtime: nodejs20.x
  resourcePolicy:
    - Effect: Allow
      Principal: "*"
      Action: execute-api:Invoke
      Resource: "arn:aws:execute-api:*"
  stage: dev`

	result := moveResourcePolicyToApiGateway(input)

	// resourcePolicy should not be at provider level
	if strings.Contains(result, "\n  resourcePolicy:") && !strings.Contains(result, "    resourcePolicy:") {
		t.Errorf("resourcePolicy still at provider level:\n%s", result)
	}

	// Should contain apiGateway section
	if !strings.Contains(result, "apiGateway:") {
		t.Errorf("apiGateway section not created:\n%s", result)
	}

	// resourcePolicy should be under apiGateway
	if !strings.Contains(result, "    resourcePolicy:") {
		t.Errorf("resourcePolicy not under apiGateway:\n%s", result)
	}
}

func TestMoveResourcePolicy_AlreadyUnderApiGateway(t *testing.T) {
	input := `provider:
  name: aws
  apiGateway:
    resourcePolicy:
      - Effect: Allow
        Principal: "*"`

	result := moveResourcePolicyToApiGateway(input)
	if result != input {
		t.Errorf("should not change when resourcePolicy is already under apiGateway")
	}
}

func TestDetectServerlessVersion(t *testing.T) {
	// Create temp dir with package.json
	dir := t.TempDir()
	pkg := map[string]interface{}{
		"devDependencies": map[string]interface{}{
			"serverless": "^3.40.0",
		},
	}
	data, _ := json.MarshalIndent(pkg, "", "  ")
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	ver := detectServerlessVersion(dir)
	if ver != 3 {
		t.Errorf("expected version 3, got %d", ver)
	}
}

func TestDetectServerlessVersion_V2(t *testing.T) {
	dir := t.TempDir()
	pkg := map[string]interface{}{
		"devDependencies": map[string]interface{}{
			"serverless": "^2.72.0",
		},
	}
	data, _ := json.MarshalIndent(pkg, "", "  ")
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	ver := detectServerlessVersion(dir)
	if ver != 2 {
		t.Errorf("expected version 2, got %d", ver)
	}
}

func TestDetectServerlessVersion_NotPresent(t *testing.T) {
	dir := t.TempDir()
	pkg := map[string]interface{}{
		"devDependencies": map[string]interface{}{
			"typescript": "^5.0.0",
		},
	}
	data, _ := json.MarshalIndent(pkg, "", "  ")
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	ver := detectServerlessVersion(dir)
	if ver != 0 {
		t.Errorf("expected version 0, got %d", ver)
	}
}

func TestUpgradeServerlessPlugins(t *testing.T) {
	dir := t.TempDir()
	pkg := map[string]interface{}{
		"devDependencies": map[string]interface{}{
			"serverless":                "^3.40.0",
			"serverless-step-functions": "^2.30.0",
			"serverless-pseudo-parameters": "^3.3.0",
		},
	}
	data, _ := json.MarshalIndent(pkg, "", "  ")
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	changed, err := upgradeServerlessPlugins(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected package.json to be modified")
	}

	// Read back and verify
	result, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	var resultPkg map[string]interface{}
	json.Unmarshal(result, &resultPkg)

	devDeps := resultPkg["devDependencies"].(map[string]interface{})
	if devDeps["serverless-step-functions"] != "^3.21.0" {
		t.Errorf("serverless-step-functions not upgraded: %v", devDeps["serverless-step-functions"])
	}
	if _, exists := devDeps["serverless-pseudo-parameters"]; exists {
		t.Error("serverless-pseudo-parameters should have been removed")
	}
}

func TestNeedsUpgrade(t *testing.T) {
	tests := []struct {
		current  string
		minimum  string
		expected bool
	}{
		{"^2.30.0", "^3.21.0", true},
		{"^3.0.0", "^3.21.0", false},
		{"^4.0.0", "^3.21.0", false},
		{"~2.5.0", "^3.0.0", true},
		{"^14.0.0", "^14.0.0", false},
		{"^12.0.0", "^14.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.current+"→"+tt.minimum, func(t *testing.T) {
			result := needsUpgrade(tt.current, tt.minimum)
			if result != tt.expected {
				t.Errorf("needsUpgrade(%q, %q) = %v, want %v", tt.current, tt.minimum, result, tt.expected)
			}
		})
	}
}

func TestTransformServerlessV3Compat_FullIntegration(t *testing.T) {
	dir := t.TempDir()

	// Create serverless.yml with all fixable issues
	slsContent := `service:
  name: my-service

plugins:
  - serverless-step-functions
  - serverless-pseudo-parameters

provider:
  name: aws
  runtime: nodejs24.x
  stage: dev
  resourcePolicy:
    - Effect: Allow
      Principal: "*"
      Action: execute-api:Invoke
      Resource: "arn:aws:execute-api:*"

functions:
  hello:
    handler: src/handler.hello
`
	os.WriteFile(filepath.Join(dir, "serverless.yml"), []byte(slsContent), 0644)

	// Create package.json with SLS v3
	pkg := map[string]interface{}{
		"devDependencies": map[string]interface{}{
			"serverless":                "^3.40.0",
			"serverless-step-functions": "^2.30.0",
			"serverless-pseudo-parameters": "^3.3.0",
		},
	}
	data, _ := json.MarshalIndent(pkg, "", "  ")
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	// Run the full compat pass
	changed, err := TransformServerlessV3Compat(dir, 24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changed) == 0 {
		t.Fatal("expected files to be changed")
	}

	// Verify serverless.yml changes
	result, _ := os.ReadFile(filepath.Join(dir, "serverless.yml"))
	content := string(result)

	// Service should be flat
	if strings.Contains(content, "  name: my-service") {
		t.Error("service should be flattened")
	}
	if !strings.Contains(content, "service: my-service") {
		t.Error("service: my-service not found")
	}

	// Runtime should be capped at 20
	if strings.Contains(content, "nodejs24.x") {
		t.Error("runtime should be capped to nodejs20.x")
	}
	if !strings.Contains(content, "nodejs20.x") {
		t.Error("nodejs20.x not found")
	}

	// serverless-pseudo-parameters should be removed
	if strings.Contains(content, "serverless-pseudo-parameters") {
		t.Error("serverless-pseudo-parameters should be removed from plugins")
	}

	// resourcePolicy should be under apiGateway
	if !strings.Contains(content, "apiGateway:") {
		t.Error("apiGateway section should exist")
	}
}

func TestTransformServerlessV3Compat_NoServerless(t *testing.T) {
	dir := t.TempDir()
	// No serverless.yml, just a package.json
	pkg := map[string]interface{}{"name": "plain-app"}
	data, _ := json.MarshalIndent(pkg, "", "  ")
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	changed, err := TransformServerlessV3Compat(dir, 24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("expected no changes for non-serverless project, got %v", changed)
	}
}

func TestTransformServerlessV3Compat_SlsV2_NoChanges(t *testing.T) {
	dir := t.TempDir()

	slsContent := `service: my-service
provider:
  runtime: nodejs18.x
`
	os.WriteFile(filepath.Join(dir, "serverless.yml"), []byte(slsContent), 0644)

	pkg := map[string]interface{}{
		"devDependencies": map[string]interface{}{
			"serverless": "^2.72.0",
		},
	}
	data, _ := json.MarshalIndent(pkg, "", "  ")
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	changed, err := TransformServerlessV3Compat(dir, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("expected no changes for SLS v2 project, got %v", changed)
	}
}
