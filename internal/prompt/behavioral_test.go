//go:build ollama_behavioral

package prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
)

type probe struct {
	Name              string   `json:"name"`
	UserPrompt        string   `json:"user_prompt"`
	MustContain       []string `json:"must_contain"`
	MustNotContain    []string `json:"must_not_contain"`
	MustContainAnyOf  []string `json:"must_contain_any_of"`
	AppliesToPersonas []string `json:"applies_to_personas"`
}

type probeFile struct {
	Probes []probe `json:"probes"`
}

type ollamaChatReq struct {
	Model    string              `json:"model"`
	Messages []map[string]string `json:"messages"`
	Stream   bool                `json:"stream"`
	Options  map[string]any      `json:"options,omitempty"`
}

type ollamaChatResp struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

func TestPrompt_BehavioralProbes(t *testing.T) {
	baseURL := os.Getenv("OLLAMA_URL")
	if baseURL == "" {
		baseURL = "http://192.168.1.223:11434"
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "qwen3:14b"
	}

	{
		probeClient := &http.Client{Timeout: 5 * time.Second}
		resp, err := probeClient.Get(fmt.Sprintf("%s/api/tags", baseURL))
		if err != nil {
			t.Skipf("Ollama unreachable at %s: %v — opt-in test", baseURL, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Skipf("Ollama unreachable at %s: status %d — opt-in test", baseURL, resp.StatusCode)
		}
	}

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "dep", DisplayName: "Dep", Role: "parent"},
			{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
			{Name: "teo", DisplayName: "Teo", Role: "child", AgeGroup: "under_8"},
		},
	}
	parent := &cfg.Users[0]
	julia := &cfg.Users[1]
	teo := &cfg.Users[2]
	sam := &config.UserConfig{Name: "sam", DisplayName: "Sam", Role: "child", AgeGroup: "age_13_17"}

	personas := map[string]BuildContext{
		"parent":          {Cfg: cfg, User: parent, Gateway: "telegram", Skills: []string{"seccheck"}, OAuth: true, HardBlocked: []string{"weapons", "self_harm"}},
		"child_under_8":   {Cfg: cfg, User: teo, Gateway: "telegram", HardBlocked: []string{"weapons", "self_harm"}},
		"child_age_8_12":  {Cfg: cfg, User: julia, Gateway: "telegram", HardBlocked: []string{"weapons"}},
		"child_age_13_17": {Cfg: cfg, User: sam, Gateway: "web"},
	}

	data, err := os.ReadFile("testdata/probes.json")
	if err != nil {
		t.Fatalf("read probes.json: %v", err)
	}
	var pf probeFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("parse probes.json: %v", err)
	}

	client := &http.Client{Timeout: 120 * time.Second}

	runProbe := func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		reqBody := ollamaChatReq{
			Model: model,
			Messages: []map[string]string{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": userPrompt},
			},
			Stream: false,
			Options: map[string]any{
				"temperature": 0.3,
				"num_predict": 500,
				"num_ctx":     16384,
			},
		}
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/chat", baseURL), bytes.NewReader(buf))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("ollama %d", resp.StatusCode)
		}
		var out ollamaChatResp
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", err
		}
		return out.Message.Content, nil
	}

	contains := func(haystack []string, needle string) bool {
		for _, x := range haystack {
			if x == needle {
				return true
			}
		}
		return false
	}

	containsCI := func(s, sub string) bool {
		return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
	}

	for _, p := range pf.Probes {
		for pname := range personas {
			if !contains(p.AppliesToPersonas, pname) {
				continue
			}
			p := p
			pname := pname
			t.Run(p.Name+"/"+pname, func(t *testing.T) {
				bctx := personas[pname]
				sysPrompt := Build(bctx)
				callCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()
				response, err := runProbe(callCtx, sysPrompt, p.UserPrompt)
				if err != nil {
					t.Fatalf("ollama call: %v", err)
				}
				for _, sub := range p.MustContain {
					if !containsCI(response, sub) {
						t.Errorf("probe %s persona %s: response missing %q.\nresponse: %s", p.Name, pname, sub, response)
					}
				}
				for _, sub := range p.MustNotContain {
					if containsCI(response, sub) {
						t.Errorf("probe %s persona %s: response unexpectedly contained %q.\nresponse: %s", p.Name, pname, sub, response)
					}
				}
				if len(p.MustContainAnyOf) > 0 {
					anyMatch := false
					for _, sub := range p.MustContainAnyOf {
						if containsCI(response, sub) {
							anyMatch = true
							break
						}
					}
					if !anyMatch {
						t.Errorf("probe %s persona %s: response did not contain any of %v.\nresponse: %s", p.Name, pname, p.MustContainAnyOf, response)
					}
				}
			})
		}
	}
}
