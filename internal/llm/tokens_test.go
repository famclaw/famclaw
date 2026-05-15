package llm

import (
	"strings"
	"testing"
)

func TestComputeMaxResponseTokens_NormalCase(t *testing.T) {
	tests := []struct {
		name          string
		contextWindow int
		promptTokens  int
		margin        int
		want          int
	}{
		{
			name:          "capped at half of context window",
			contextWindow: 65536,
			promptTokens:  5000,
			margin:        1024,
			want:          32768, // raw=59512, ceiling=32768
		},
		{
			name:          "small prompt leaves room for response",
			contextWindow: 8192,
			promptTokens:  1000,
			margin:        512,
			want:          4096, // raw=6680, ceiling=4096
		},
		{
			name:          "large prompt still gets ceiling",
			contextWindow: 4096,
			promptTokens:  500,
			margin:        256,
			want:          2048, // raw=3340, ceiling=2048
		},
		{
			name:          "prompt plus margin exactly fills half",
			contextWindow: 2048,
			promptTokens:  512,
			margin:        512,
			want:          1024, // raw=1024 == ceiling
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeMaxResponseTokens(tt.contextWindow, tt.promptTokens, tt.margin)
			if got != tt.want {
				t.Errorf("ComputeMaxResponseTokens(%d, %d, %d) = %d, want %d",
					tt.contextWindow, tt.promptTokens, tt.margin, got, tt.want)
			}
		})
	}
}

func TestComputeMaxResponseTokens_FloorAt256(t *testing.T) {
	tests := []struct {
		name          string
		contextWindow int
		promptTokens  int
		margin        int
		want          int
	}{
		{
			name:          "small ctx triggers floor",
			contextWindow: 1500,
			promptTokens:  1200,
			margin:        100,
			want:          256, // raw=200, floored to 256; ceiling=750 > 256 so floor wins
		},
		{
			name:          "negative raw triggers floor",
			contextWindow: 1024,
			promptTokens:  900,
			margin:        200,
			want:          256, // raw=-76, floored to 256; ceiling=512 > 256
		},
		{
			name:          "zero raw triggers floor",
			contextWindow: 2048,
			promptTokens:  1536,
			margin:        512,
			want:          256, // raw=0, floored to 256; ceiling=1024 > 256
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeMaxResponseTokens(tt.contextWindow, tt.promptTokens, tt.margin)
			if got != tt.want {
				t.Errorf("ComputeMaxResponseTokens(%d, %d, %d) = %d, want %d",
					tt.contextWindow, tt.promptTokens, tt.margin, got, tt.want)
			}
		})
	}
}

func TestComputeMaxResponseTokens_ZeroCtx(t *testing.T) {
	got := ComputeMaxResponseTokens(0, 1000, 512)
	if got != 2048 {
		t.Errorf("contextWindow=0: got %d, want 2048", got)
	}
}

func TestComputeMaxResponseTokens_NegativeCtx(t *testing.T) {
	got := ComputeMaxResponseTokens(-1, 1000, 512)
	if got != 2048 {
		t.Errorf("contextWindow=-1: got %d, want 2048", got)
	}
}

func TestEstimatePromptTokens_Empty(t *testing.T) {
	got := EstimatePromptTokens(nil)
	if got != 0 {
		t.Errorf("nil messages: got %d, want 0", got)
	}
	got = EstimatePromptTokens([]Message{})
	if got != 0 {
		t.Errorf("empty slice: got %d, want 0", got)
	}
}

func TestEstimatePromptTokens_SingleMessage(t *testing.T) {
	content := strings.Repeat("a", 100) // 100 chars
	msgs := []Message{{Role: "user", Content: content}}
	got := EstimatePromptTokens(msgs)
	// 100 chars / 4 = 25 (rounded up via (100+3)/4 = 25)
	want := 25
	if got != want {
		t.Errorf("100-char message: got %d, want %d", got, want)
	}
}

func TestEstimatePromptTokens_MultipleMessages(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		want     int
	}{
		{
			name: "two messages totaling 80 chars",
			messages: []Message{
				{Role: "system", Content: strings.Repeat("s", 40)},
				{Role: "user", Content: strings.Repeat("u", 40)},
			},
			want: 20, // 80/4 = 20
		},
		{
			name: "three messages, sum divided by 4",
			messages: []Message{
				{Role: "system", Content: strings.Repeat("x", 100)},
				{Role: "user", Content: strings.Repeat("y", 100)},
				{Role: "assistant", Content: strings.Repeat("z", 100)},
			},
			want: 75, // (300+3)/4 = 75
		},
		{
			name: "messages with odd total",
			messages: []Message{
				{Role: "user", Content: strings.Repeat("a", 101)},
			},
			want: 26, // (101+3)/4 = 26
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimatePromptTokens(tt.messages)
			if got != tt.want {
				t.Errorf("%s: got %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}
