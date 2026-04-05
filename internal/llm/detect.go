package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Known context window sizes for cloud models.
var knownContextWindows = map[string]int{
	// OpenAI
	"gpt-4o":          128000,
	"gpt-4o-mini":     128000,
	"gpt-4.1-mini":    1000000,
	"gpt-4.1-nano":    1000000,
	"o4-mini":         200000,
	// Groq (hosted models)
	"llama-3.3-70b-versatile": 128000,
	"llama-3.1-8b-instant":    131072,
	"gemma2-9b-it":            8192,
	"mixtral-8x7b-32768":      32768,
	// OpenRouter free
	"google/gemma-3-4b-it:free":                 8192,
	"meta-llama/llama-3.3-70b-instruct:free":    131072,
	"qwen/qwen3-8b:free":                        40960,
	// Ollama defaults (can be overridden by /api/show)
	"gemma4:e2b":  131072,
	"gemma4:e4b":  131072,
	"llama3.2:3b": 131072,
	"llama3.1:8b": 131072,
	"qwen3:4b":    40960,
	"qwen3:8b":    40960,
	"phi4-mini":   16384,
	"mistral:7b":  32768,
	"tinyllama":   2048,
}

// DetectContextWindow queries the backend for the model's context window size.
// Priority: Ollama /api/show → llama.cpp /props → known model lookup → default.
func DetectContextWindow(ctx context.Context, baseURL, model, apiKey string) int {
	baseURL = strings.TrimRight(baseURL, "/")

	// Try Ollama /api/show
	if n := detectOllamaContext(ctx, baseURL, model, apiKey); n > 0 {
		return n
	}

	// Try llama.cpp /props
	if n := detectLlamaCppContext(ctx, baseURL, apiKey); n > 0 {
		return n
	}

	// Known model lookup
	if n, ok := knownContextWindows[model]; ok {
		return n
	}

	return 4096 // safe default
}

func detectOllamaContext(ctx context.Context, baseURL, model, apiKey string) int {
	ollamaBase := strings.TrimSuffix(baseURL, "/v1")
	url := ollamaBase + "/api/show"

	body := fmt.Sprintf(`{"name":%q}`, model)
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0
	}
	defer resp.Body.Close()

	var result struct {
		ModelInfo struct {
			ContextLength int `json:"context_length"`
		} `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0
	}
	return result.ModelInfo.ContextLength
}

func detectLlamaCppContext(ctx context.Context, baseURL, apiKey string) int {
	url := strings.TrimSuffix(baseURL, "/v1") + "/props"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0
	}
	defer resp.Body.Close()

	var result struct {
		DefaultGenSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0
	}
	return result.DefaultGenSettings.NCtx
}
