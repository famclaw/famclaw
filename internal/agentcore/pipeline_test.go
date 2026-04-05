package agentcore

import (
	"context"
	"errors"
	"testing"
)

func TestPipelineRun(t *testing.T) {
	var order []string

	s1 := func(_ context.Context, turn *Turn) error {
		order = append(order, "s1")
		turn.SetMeta("step1", true)
		return nil
	}
	s2 := func(_ context.Context, turn *Turn) error {
		order = append(order, "s2")
		// Verify s1 ran first
		if v, ok := turn.GetMeta("step1"); !ok || v != true {
			t.Error("s2: expected step1 metadata from s1")
		}
		turn.Output = "done"
		return nil
	}
	s3 := func(_ context.Context, turn *Turn) error {
		order = append(order, "s3")
		return nil
	}

	p := Pipeline{s1, s2, s3}
	turn := &Turn{Input: "hello"}

	if err := p.Run(context.Background(), turn); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(order) != 3 {
		t.Errorf("expected 3 stages, ran %d", len(order))
	}
	if order[0] != "s1" || order[1] != "s2" || order[2] != "s3" {
		t.Errorf("wrong order: %v", order)
	}
	if turn.Output != "done" {
		t.Errorf("output = %q, want 'done'", turn.Output)
	}
}

func TestPipelineAbortOnError(t *testing.T) {
	errBoom := errors.New("boom")
	var order []string

	s1 := func(_ context.Context, turn *Turn) error {
		order = append(order, "s1")
		return nil
	}
	s2 := func(_ context.Context, turn *Turn) error {
		order = append(order, "s2")
		return errBoom
	}
	s3 := func(_ context.Context, turn *Turn) error {
		order = append(order, "s3")
		t.Error("s3 should not run after s2 error")
		return nil
	}

	p := Pipeline{s1, s2, s3}
	turn := &Turn{}

	err := p.Run(context.Background(), turn)
	if !errors.Is(err, errBoom) {
		t.Errorf("expected errBoom, got %v", err)
	}
	if len(order) != 2 {
		t.Errorf("expected 2 stages to run, got %d: %v", len(order), order)
	}
}

func TestPipelineCancelledContext(t *testing.T) {
	ran := false
	s1 := func(_ context.Context, turn *Turn) error {
		ran = true
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before running

	p := Pipeline{s1}
	err := p.Run(ctx, &Turn{})
	if err == nil {
		t.Fatal("expected context error")
	}
	if ran {
		t.Error("stage should not run with cancelled context")
	}
}

func TestPipelineNilStage(t *testing.T) {
	p := Pipeline{
		func(_ context.Context, _ *Turn) error { return nil },
		nil, // should be caught
		func(_ context.Context, _ *Turn) error { t.Error("should not run"); return nil },
	}
	err := p.Run(context.Background(), &Turn{})
	if err == nil {
		t.Fatal("expected error for nil stage")
	}
}

func TestPipelineEmpty(t *testing.T) {
	p := Pipeline{}
	err := p.Run(context.Background(), &Turn{})
	if err != nil {
		t.Fatalf("empty pipeline should not error: %v", err)
	}
}

func TestPipelineLen(t *testing.T) {
	p := Pipeline{
		func(_ context.Context, _ *Turn) error { return nil },
		func(_ context.Context, _ *Turn) error { return nil },
	}
	if p.Len() != 2 {
		t.Errorf("Len() = %d, want 2", p.Len())
	}
}

func TestPipelineAppend(t *testing.T) {
	s1 := func(_ context.Context, _ *Turn) error { return nil }
	s2 := func(_ context.Context, _ *Turn) error { return nil }
	s3 := func(_ context.Context, _ *Turn) error { return nil }

	p := Pipeline{s1}.Append(s2, s3)
	if p.Len() != 3 {
		t.Errorf("Append: Len() = %d, want 3", p.Len())
	}
}

func TestPipelinePrepend(t *testing.T) {
	var order []string
	s1 := func(_ context.Context, _ *Turn) error { order = append(order, "s1"); return nil }
	s2 := func(_ context.Context, _ *Turn) error { order = append(order, "s2"); return nil }
	s3 := func(_ context.Context, _ *Turn) error { order = append(order, "s3"); return nil }

	p := Pipeline{s2}.Prepend(s1).Append(s3)
	if err := p.Run(context.Background(), &Turn{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(order) != 3 || order[0] != "s1" || order[1] != "s2" || order[2] != "s3" {
		t.Errorf("wrong order: %v", order)
	}
}

func TestTurnMetadata(t *testing.T) {
	turn := &Turn{}

	// Get from nil map
	v, ok := turn.GetMeta("missing")
	if ok || v != nil {
		t.Error("expected nil/false from empty metadata")
	}

	// Set initializes the map
	turn.SetMeta("key", "value")
	v, ok = turn.GetMeta("key")
	if !ok || v != "value" {
		t.Errorf("expected 'value', got %v (ok=%v)", v, ok)
	}

	// Overwrite
	turn.SetMeta("key", 42)
	v, _ = turn.GetMeta("key")
	if v != 42 {
		t.Errorf("expected 42, got %v", v)
	}
}

func TestToolAllowedForRole(t *testing.T) {
	tests := []struct {
		name  string
		roles []string
		role  string
		want  bool
	}{
		{"empty roles allows all", nil, "child", true},
		{"explicit role match", []string{"parent", "child"}, "child", true},
		{"role not in list", []string{"parent"}, "child", false},
		{"parent allowed", []string{"parent"}, "parent", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &Tool{Name: "test", Roles: tt.roles}
			if got := tool.AllowedForRole(tt.role); got != tt.want {
				t.Errorf("AllowedForRole(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}
