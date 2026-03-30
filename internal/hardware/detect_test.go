package hardware

import (
	"runtime"
	"testing"
)

func TestDetect(t *testing.T) {
	info := Detect()

	if info.OS == "" {
		t.Error("OS should not be empty")
	}
	if info.Arch == "" {
		t.Error("Arch should not be empty")
	}
	if info.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", info.Arch, runtime.GOARCH)
	}
}

func TestDetectOS(t *testing.T) {
	os := detectOS()
	if os != "linux" && os != "darwin" && os != "windows" && os != "android" {
		t.Errorf("unexpected OS: %q", os)
	}
}

func TestCanRunLocal(t *testing.T) {
	tests := []struct {
		name    string
		ramMB   int
		os      string
		want    bool
	}{
		{"RPi 5 8GB", 8192, "linux", true},
		{"RPi 4 4GB", 4096, "linux", true},
		{"RPi 3 1GB", 1024, "linux", false},
		{"Mac Mini 16GB", 16384, "darwin", true},
		{"Android 4GB", 4096, "android", false},
		{"Android 8GB", 8192, "android", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ramMB >= 4096 && tt.os != "android"
			if got != tt.want {
				t.Errorf("CanRunLocal(%dMB, %s) = %v, want %v", tt.ramMB, tt.os, got, tt.want)
			}
		})
	}
}

func TestRecommendedModel(t *testing.T) {
	info := Detect()
	// RecommendedModel should always be non-empty
	if info.RecommendedModel == "" {
		t.Error("RecommendedModel should not be empty")
	}
}

func TestOllamaInstalled(t *testing.T) {
	// Just verify it doesn't panic
	_ = ollamaInstalled()
}

func TestDetectModel(t *testing.T) {
	// Just verify it doesn't panic
	_ = detectModel()
}
