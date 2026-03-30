// Package hardware detects system capabilities to decide if local LLM is viable.
package hardware

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/famclaw/famclaw/internal/llm"
)

// HardwareInfo describes the current device's capabilities.
type HardwareInfo struct {
	OS               string `json:"os"`                // linux | darwin | android
	Arch             string `json:"arch"`              // arm64 | arm | amd64
	TotalRAMMB       int    `json:"total_ram_mb"`
	Model            string `json:"model"`             // "Raspberry Pi 5 Model B" or "Mac14,3" or ""
	OllamaFound      bool   `json:"ollama_found"`
	CanRunLocal      bool   `json:"can_run_local"`     // true if RAM >= 4096 and not Android
	RecommendedModel string `json:"recommended_model"` // from llm.HardwareRecommendation()
}

// Detect returns information about the current hardware.
func Detect() HardwareInfo {
	info := HardwareInfo{
		OS:   detectOS(),
		Arch: runtime.GOARCH,
	}

	info.TotalRAMMB = detectRAM()
	info.Model = detectModel()
	info.OllamaFound = ollamaInstalled()
	info.CanRunLocal = info.TotalRAMMB >= 4096 && info.OS != "android"
	info.RecommendedModel = llm.HardwareRecommendation(info.TotalRAMMB)

	return info
}

func detectOS() string {
	goos := runtime.GOOS
	if goos == "linux" {
		// Check if Android (Termux sets PREFIX)
		if os.Getenv("PREFIX") != "" && strings.Contains(os.Getenv("PREFIX"), "com.termux") {
			return "android"
		}
	}
	return goos
}

func detectRAM() int {
	switch runtime.GOOS {
	case "linux":
		return detectRAMLinux()
	case "darwin":
		return detectRAMDarwin()
	default:
		return 0
	}
}

func detectRAMLinux() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.Atoi(fields[1])
				if err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

func detectRAMDarwin() int {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return int(bytes / 1024 / 1024)
}

func detectModel() string {
	switch runtime.GOOS {
	case "linux":
		// RPi: /proc/device-tree/model
		data, err := os.ReadFile("/proc/device-tree/model")
		if err == nil {
			return strings.TrimRight(string(data), "\x00\n")
		}
		return ""
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.model").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		return ""
	default:
		return ""
	}
}

func ollamaInstalled() bool {
	_, err := exec.LookPath("ollama")
	return err == nil
}
