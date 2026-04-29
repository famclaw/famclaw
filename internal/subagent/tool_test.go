package subagent

import "testing"

func TestSpawnAgentTool_Schema(t *testing.T) {
	tool := SpawnAgentTool()

	if tool.Name != "builtin__spawn_agent" {
		t.Errorf("name = %q, want %q", tool.Name, "builtin__spawn_agent")
	}
	if tool.Source != "builtin" {
		t.Errorf("source = %q, want %q", tool.Source, "builtin")
	}
	if len(tool.Roles) != 1 || tool.Roles[0] != "parent" {
		t.Errorf("roles = %v, want [parent]", tool.Roles)
	}

	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("InputSchema missing 'properties'")
	}
	if _, ok := props["prompt"]; !ok {
		t.Error("missing 'prompt' in schema properties")
	}
	if _, ok := props["profile"]; !ok {
		t.Error("missing 'profile' in schema properties")
	}
	if _, ok := props["max_turns"]; !ok {
		t.Error("missing 'max_turns' in schema properties")
	}

	required, ok := tool.InputSchema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "prompt" {
		t.Errorf("required = %v, want [prompt]", required)
	}
}

func TestSpawnAgentTool_AllowedForRole(t *testing.T) {
	tool := SpawnAgentTool()

	if !tool.AllowedForRole("parent") {
		t.Error("parent should be allowed")
	}
	if tool.AllowedForRole("child") {
		t.Error("child should NOT be allowed")
	}
	if tool.AllowedForRole("") {
		t.Error("empty role should NOT be allowed")
	}
}
