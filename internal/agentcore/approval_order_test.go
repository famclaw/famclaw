package agentcore

import (
	"testing"
	"time"
)

func TestLaterQualifiedApprovalRequiresOrderedValidDecision(t *testing.T) {
	now := time.Now()
	legacy := map[string]any{"status": "denied", "decided_by": "parent", "decision_sequence": int64(4)}
	valid := map[string]any{"status": "approved", "decided_by": "parent-link", "decision_sequence": int64(5), "expires_at": now.Add(time.Hour)}
	tests := []struct {
		name   string
		change func(map[string]any, map[string]any)
		want   bool
	}{
		{"later", func(_, _ map[string]any) {}, true},
		{"reverse", func(l, q map[string]any) { l["decision_sequence"] = int64(6) }, false},
		{"tie", func(l, q map[string]any) { q["decision_sequence"] = int64(4) }, false},
		{"historical denial and new grant", func(l, q map[string]any) { l["decision_sequence"] = int64(0) }, true},
		{"two historical decisions", func(l, q map[string]any) { l["decision_sequence"] = int64(0); q["decision_sequence"] = int64(0) }, false},
		{"negative legacy", func(l, q map[string]any) { l["decision_sequence"] = int64(-1) }, false},
		{"pending", func(l, q map[string]any) { q["status"] = "pending" }, false},
		{"denied", func(l, q map[string]any) { q["status"] = "denied" }, false},
		{"expired", func(l, q map[string]any) { q["expires_at"] = now }, false},
		{"missing expiry", func(l, q map[string]any) { delete(q, "expires_at") }, false},
		{"malformed sequence", func(l, q map[string]any) { q["decision_sequence"] = "5" }, false},
		{"missing decision actor", func(l, q map[string]any) { q["decided_by"] = "" }, false},
		{"missing legacy actor", func(l, q map[string]any) { delete(l, "decided_by") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := map[string]any{}
			q := map[string]any{}
			for k, v := range legacy {
				l[k] = v
			}
			for k, v := range valid {
				q[k] = v
			}
			tt.change(l, q)
			if got := laterQualifiedApproval(q, l, now); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
