package inference

// ModelInfo describes a recommended GGUF model.
type ModelInfo struct {
	Name        string // display name
	Filename    string // GGUF filename
	URL         string // HuggingFace download URL
	SHA256      string // expected hash for verification
	SizeMB      int    // approximate file size
	MinRAMMB    int    // minimum RAM to run
	ContextSize int    // default context window
	ToolSupport bool   // whether the model supports tool calling
}

// RecommendedModels returns models appropriate for the given RAM.
func RecommendedModels(ramMB int) []ModelInfo {
	var models []ModelInfo
	for _, m := range modelCatalog {
		if ramMB >= m.MinRAMMB {
			models = append(models, m)
		}
	}
	return models
}

// DefaultModel returns the best model for the given RAM.
func DefaultModel(ramMB int) *ModelInfo {
	// Find the largest model that fits
	var best *ModelInfo
	for i := range modelCatalog {
		m := &modelCatalog[i]
		if ramMB >= m.MinRAMMB {
			if best == nil || m.MinRAMMB > best.MinRAMMB {
				best = m
			}
		}
	}
	return best
}

// modelCatalog is the embedded list of recommended GGUF models.
// URLs point to HuggingFace repos. SHA256 should be verified after download.
var modelCatalog = []ModelInfo{
	{
		Name:        "Qwen 2.5 1.5B (tiny, chat only)",
		Filename:    "qwen2.5-1.5b-instruct-q4_k_m.gguf",
		MinRAMMB:    1024,
		ContextSize: 4096,
		SizeMB:      1100,
		ToolSupport: false,
	},
	{
		Name:        "Phi-4 Mini (small, fast)",
		Filename:    "phi-4-mini-instruct-q4_k_m.gguf",
		MinRAMMB:    2048,
		ContextSize: 16384,
		SizeMB:      2300,
		ToolSupport: true,
	},
	{
		Name:        "Qwen3 4B (balanced)",
		Filename:    "qwen3-4b-q4_k_m.gguf",
		MinRAMMB:    4096,
		ContextSize: 40960,
		SizeMB:      2800,
		ToolSupport: true,
	},
	{
		Name:        "Gemma 4 E2B (recommended)",
		Filename:    "gemma-4-e2b-it-q4_k_m.gguf",
		MinRAMMB:    8192,
		ContextSize: 131072,
		SizeMB:      5400,
		ToolSupport: true,
	},
	{
		Name:        "Llama 3.1 8B (powerful)",
		Filename:    "llama-3.1-8b-instruct-q4_k_m.gguf",
		MinRAMMB:    8192,
		ContextSize: 131072,
		SizeMB:      4700,
		ToolSupport: true,
	},
}
