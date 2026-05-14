package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to an Ollama-compatible (OpenAI chat/completions) API.
type Client struct {
	BaseURL string
	Model   string
	client  *http.Client
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
	// Ollama native format (non-OpenAI compat)
	Message chatMessage `json:"message"`
}

func NewClient(baseURL, model string) *Client {
	return &Client{
		BaseURL: baseURL,
		Model:   model,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Chat sends a system + user message and returns the assistant reply.
func (c *Client) Chat(system, user string) (string, error) {
	msgs := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}

	body, err := json.Marshal(chatRequest{
		Model:    c.Model,
		Messages: msgs,
		Stream:   false,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// Try OpenAI-compat endpoint first, fall back to Ollama native
	resp, err := c.post("/v1/chat/completions", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	// OpenAI format: choices[0].message.content
	if len(chatResp.Choices) > 0 {
		return chatResp.Choices[0].Message.Content, nil
	}

	// Ollama native format: message.content
	if chatResp.Message.Content != "" {
		return chatResp.Message.Content, nil
	}

	return "", fmt.Errorf("empty response from LLM")
}

// Ping checks if the LLM server is reachable.
func (c *Client) Ping() error {
	resp, err := c.client.Get(c.BaseURL + "/api/tags")
	if err != nil {
		return fmt.Errorf("LLM server unreachable at %s: %w", c.BaseURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LLM server returned %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) post(path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}
