package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// IsOllamaURL returns true if the base URL looks like a local Ollama instance.
func IsOllamaURL(baseURL string) bool {
	lower := strings.ToLower(baseURL)
	return strings.Contains(lower, "localhost:11434") ||
		strings.Contains(lower, "127.0.0.1:11434") ||
		strings.Contains(lower, ":11434")
}

// Ping checks if the Ollama server is reachable and the model is available.
// For non-Ollama backends, this is a no-op (returns nil).
func (c *Client) Ping(ctx context.Context) error {
	if !IsOllamaURL(c.baseURL) {
		return nil
	}

	// Ollama-specific: GET /api/tags lists available models
	ollamaBase := strings.TrimRight(c.baseURL, "/")
	// Strip /v1 suffix if present to get the Ollama root
	ollamaBase = strings.TrimSuffix(ollamaBase, "/v1")

	req, err := http.NewRequestWithContext(ctx, "GET", ollamaBase+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("creating ping request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Ollama not reachable at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil // server is up, just can't parse response
	}

	for _, m := range result.Models {
		if m.Name == c.model || m.Name == c.model+":latest" {
			return nil
		}
	}
	return fmt.Errorf("model %q not found in Ollama — run: ollama pull %s", c.model, c.model)
}

// OllamaModels lists available models from an Ollama server.
// Returns nil for non-Ollama backends.
func (c *Client) OllamaModels(ctx context.Context) ([]string, error) {
	if !IsOllamaURL(c.baseURL) {
		return nil, nil
	}

	ollamaBase := strings.TrimSuffix(strings.TrimRight(c.baseURL, "/"), "/v1")
	req, err := http.NewRequestWithContext(ctx, "GET", ollamaBase+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("creating models request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama not reachable: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil
	}

	var names []string
	for _, m := range result.Models {
		names = append(names, m.Name)
	}
	return names, nil
}
