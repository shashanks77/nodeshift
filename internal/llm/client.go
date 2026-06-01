package llm

import (
	"fmt"
)

// Client wraps multiple providers with automatic fallback.
// Primary: GitHub Models (Claude Opus). Fallback: Ollama (local).
type Client struct {
	providers []Provider
	active    Provider
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Message chatMessage  `json:"message"`
}

// NewClient creates a client with GitHub Models as primary and Ollama as fallback.
// If githubToken is provided, GitHub Models is tried first.
// If ollamaURL is provided (or defaults to localhost:11434), Ollama is the fallback.
func NewClient(githubToken, githubModel, ollamaURL, ollamaModel string) *Client {
	c := &Client{}

	if githubToken != "" {
		c.providers = append(c.providers, NewGitHubModelsProvider(githubToken, githubModel))
	}

	// Always add Ollama as fallback
	c.providers = append(c.providers, NewOllamaProvider(ollamaURL, ollamaModel))

	return c
}

// Chat sends a message through the first available provider, falling back on error.
func (c *Client) Chat(system, user string) (string, error) {
	var lastErr error
	for _, p := range c.providers {
		result, err := p.Chat(system, user)
		if err == nil {
			c.active = p
			return result, nil
		}
		lastErr = err
		fmt.Printf("  [LLM] %s failed: %v, trying next provider...\n", p.Name(), err)
	}
	return "", fmt.Errorf("all LLM providers failed: %w", lastErr)
}

// Ping checks if at least one provider is available.
func (c *Client) Ping() error {
	for _, p := range c.providers {
		if p.Available() {
			c.active = p
			return nil
		}
	}
	return fmt.Errorf("no LLM provider available")
}

// ActiveProvider returns the name of the currently active provider.
func (c *Client) ActiveProvider() string {
	if c.active != nil {
		return c.active.Name()
	}
	if len(c.providers) > 0 {
		return c.providers[0].Name()
	}
	return "none"
}
