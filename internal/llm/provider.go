package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Provider is an LLM backend that can handle chat completions.
type Provider interface {
	Name() string
	Chat(system, user string) (string, error)
	Available() bool
}

// ────────────────────────────────────────────────────────────────────────────
// GitHub Models Provider (Claude Opus via GitHub Models API)
// ────────────────────────────────────────────────────────────────────────────

type GitHubModelsProvider struct {
	Token  string
	Model  string
	client *http.Client
}

func NewGitHubModelsProvider(token, model string) *GitHubModelsProvider {
	p := &GitHubModelsProvider{
		Token: token,
		Model: model,
		client: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
	// If no model specified, auto-detect the best available
	if model == "" && token != "" {
		if best := p.detectBestModel(); best != "" {
			p.Model = best
		} else {
			p.Model = "gpt-4o" // safe fallback
		}
	}
	return p
}

// modelPreference defines priority order: best models for code tasks first.
var modelPreference = []string{
	"gpt-4o",
	"Meta-Llama-3.1-405B-Instruct",
	"gpt-4o-mini",
	"Meta-Llama-3.1-8B-Instruct",
}

// detectBestModel queries GitHub Models API and returns the highest-ranked
// available chat-completion model.
func (g *GitHubModelsProvider) detectBestModel() string {
	req, err := http.NewRequest("GET", "https://models.inference.ai.azure.com/models", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)

	resp, err := g.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var models []struct {
		Name string `json:"name"`
		Task string `json:"task"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return ""
	}

	// Build set of available chat models
	available := make(map[string]bool)
	for _, m := range models {
		if m.Task == "chat-completion" {
			available[m.Name] = true
		}
	}

	// Return highest preference that's available
	for _, pref := range modelPreference {
		if available[pref] {
			return pref
		}
	}

	// If none from our list, pick any chat model
	for _, m := range models {
		if m.Task == "chat-completion" {
			return m.Name
		}
	}
	return ""
}

func (g *GitHubModelsProvider) Name() string {
	return fmt.Sprintf("github-models (%s)", g.Model)
}

func (g *GitHubModelsProvider) Available() bool {
	if g.Token == "" {
		return false
	}
	// Quick check: list models endpoint
	req, err := http.NewRequest("GET", "https://models.inference.ai.azure.com/models", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	resp, err := g.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (g *GitHubModelsProvider) Chat(system, user string) (string, error) {
	msgs := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}

	body, err := json.Marshal(chatRequest{
		Model:    g.Model,
		Messages: msgs,
		Stream:   false,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://models.inference.ai.azure.com/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.Token)

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github models request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github models API %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Choices) > 0 {
		return chatResp.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("empty response from GitHub Models")
}

// ────────────────────────────────────────────────────────────────────────────
// Ollama Provider (local, with auto-pull)
// ────────────────────────────────────────────────────────────────────────────

type OllamaProvider struct {
	BaseURL string
	Model   string
	client  *http.Client
}

func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "qwen2.5-coder:7b"
	}
	return &OllamaProvider{
		BaseURL: baseURL,
		Model:   model,
		client: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

func (o *OllamaProvider) Name() string {
	return fmt.Sprintf("ollama (%s)", o.Model)
}

func (o *OllamaProvider) Available() bool {
	resp, err := o.client.Get(o.BaseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (o *OllamaProvider) Chat(system, user string) (string, error) {
	// Ensure model is available (auto-pull if missing)
	if err := o.ensureModel(); err != nil {
		return "", fmt.Errorf("ollama model setup: %w", err)
	}

	msgs := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}

	body, err := json.Marshal(chatRequest{
		Model:    o.Model,
		Messages: msgs,
		Stream:   false,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", o.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama API %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Choices) > 0 {
		return chatResp.Choices[0].Message.Content, nil
	}
	if chatResp.Message.Content != "" {
		return chatResp.Message.Content, nil
	}
	return "", fmt.Errorf("empty response from Ollama")
}

// ensureModel checks if model is locally available, pulls if not.
func (o *OllamaProvider) ensureModel() error {
	resp, err := o.client.Get(o.BaseURL + "/api/tags")
	if err != nil {
		return fmt.Errorf("cannot reach Ollama: %w", err)
	}
	defer resp.Body.Close()

	var tagsResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return fmt.Errorf("parse tags: %w", err)
	}

	// Check if model already exists
	for _, m := range tagsResp.Models {
		if m.Name == o.Model || strings.HasPrefix(m.Name, o.Model+":") || strings.HasPrefix(o.Model, m.Name) {
			return nil
		}
	}

	// Model not found — pull it
	fmt.Printf("  [LLM] Model %q not found locally, pulling...\n", o.Model)
	cmd := exec.Command("ollama", "pull", o.Model)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to pull model %s: %w", o.Model, err)
	}
	fmt.Printf("  [LLM] Model %q ready\n", o.Model)
	return nil
}
