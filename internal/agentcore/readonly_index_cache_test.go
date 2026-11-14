package agentcore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/llm"
)

func TestReadOnlyIndexCacheMetadataChanges(t *testing.T) {
	tools := []Tool{{Name: "mcp__a__lookup", ReadOnly: true}, {Name: "mcp__b__lookup", ReadOnly: false}, {Name: "builtin__echo", ReadOnly: true}}
	var c readOnlyIndexCache
	for _, step := range []struct {
		name   string
		change func()
	}{
		{"initial collision", func() {}},
		{"in-place flag", func() { tools[0].ReadOnly = false }},
		{"rename", func() { tools[1].Name = "mcp__b__other" }},
		{"append", func() { tools = append(tools, Tool{Name: "lookup", ReadOnly: true}) }},
		{"remove", func() { tools = tools[:2] }},
		{"duplicate exact", func() { tools = append(tools, Tool{Name: tools[0].Name, ReadOnly: true}) }},
	} {
		t.Run(step.name, func(t *testing.T) {
			step.change()
			got := c.get(tools)
			want := buildReadOnlyIndex(tools)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}

func TestReadOnlyIndexCacheWarmAllocations(t *testing.T) {
	tools := []Tool{{Name: "mcp__one__lookup", ReadOnly: true}, {Name: "builtin__echo"}}
	var c readOnlyIndexCache
	_ = c.get(tools)
	if n := testing.AllocsPerRun(100, func() { _ = turnReadOnlyLookup(c.get(tools), "lookup") }); n != 0 {
		t.Fatalf("warm metadata lookup allocations = %v, want 0", n)
	}
}

func TestToolLoopFinalWarningUsesCurrentToolMetadata(t *testing.T) {
	turn := newRebuildTurn("go")
	chatter := newStubChatter(0)
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
		turn.Tools[0].ReadOnly = true
		return "", errors.New("test failure")
	}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turn.Output, "marked read-only") {
		t.Fatalf("warning did not use changed metadata: %q", turn.Output)
	}
}

func BenchmarkReadOnlyIndexCacheWarmLookup(b *testing.B) {
	tools := []Tool{{Name: "mcp__one__lookup", ReadOnly: true}, {Name: "builtin__echo"}}
	var c readOnlyIndexCache
	_ = c.get(tools)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = turnReadOnlyLookup(c.get(tools), "lookup")
	}
}
