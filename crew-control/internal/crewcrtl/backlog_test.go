package crewcrtl

import (
	"strings"
	"testing"
)

// TestParseBacklog tests the backlog parser with a sample backlog that
// mirrors the real data/backlog.md format.
func TestParseBacklog(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantInFlight int
		wantQueued   int
		wantItemIDs  []string // IDs that must appear in the combined output
		wantRepos    []string // repos that must appear
	}{
		{
			name:         "empty",
			input:        "",
			wantInFlight: 0,
			wantQueued:   0,
		},
		{
			name: "simple in-flight and queued",
			input: `# Backlog

## In flight
- [ ] fc-crew-control-mcp - ship: read-only crew-control HTTP MCP server (repo: famclaw) (kind: ship) (since 2026-07-31)
- [ ] fc-vision-plus-tools - BLOCKER for the captain's use case (repo: famclaw) (kind: scout) (since 2026-07-31)

## Queued
- [ ] fc-sandbox-per-convo - ship: partition the file sandbox (repo: famclaw) (kind: ship) (since 2026-07-30)

## Done
- [x] fc-299-fix - ship: fix a bug (repo: famclaw) (kind: ship)
`,
			wantInFlight: 2,
			wantQueued:   1,
			wantItemIDs:  []string{"fc-crew-control-mcp", "fc-vision-plus-tools", "fc-sandbox-per-convo"},
		},
		{
			name: "item with continuation line",
			input: `## In flight
- [ ] fc-test - ship: test item (repo: famclaw) (kind: ship) (since 2026-07-31)
  This is a continuation line that should be skipped.
  And another one.

## Queued
`,
			wantInFlight: 1,
			wantQueued:   0,
		},
		{
			name: "item with blocked-by",
			input: `## In flight
- [ ] fc-sandbox-per-convo - ship: partition sandbox blocked-by: fc-childapprove-t15 (repo: famclaw) (kind: ship) (since 2026-07-30)

## Queued
`,
			wantInFlight: 1,
			wantQueued:   0,
			wantItemIDs:  []string{"fc-sandbox-per-convo"},
			wantRepos:    []string{"famclaw"},
		},
		{
			name: "no in-flight section",
			input: `## Queued
- [ ] fc-queued - queued item (repo: famclaw)

## Done
`,
			wantInFlight: 0,
			wantQueued:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bl, err := ParseBacklog(tt.input)
			if err != nil {
				t.Fatalf("ParseBacklog error: %v", err)
			}
			if len(bl.InFlight) != tt.wantInFlight {
				t.Errorf("InFlight: got %d, want %d", len(bl.InFlight), tt.wantInFlight)
			}
			if len(bl.Queued) != tt.wantQueued {
				t.Errorf("Queued: got %d, want %d", len(bl.Queued), tt.wantQueued)
			}

			// Check that expected IDs appear.
			allIDs := make(map[string]bool)
			for _, item := range bl.InFlight {
				allIDs[item.ID] = true
			}
			for _, item := range bl.Queued {
				allIDs[item.ID] = true
			}
			for _, id := range tt.wantItemIDs {
				if !allIDs[id] {
					t.Errorf("expected ID %q not found in parsed items", id)
				}
			}

			// Check that expected repos appear.
			allRepos := make(map[string]bool)
			for _, item := range bl.InFlight {
				allRepos[item.Repo] = true
			}
			for _, item := range bl.Queued {
				allRepos[item.Repo] = true
			}
			for _, repo := range tt.wantRepos {
				if !allRepos[repo] {
					t.Errorf("expected repo %q not found in parsed items", repo)
				}
			}
		})
	}
}

// TestParseBacklog_MetadataExtraction verifies that metadata fields are
// correctly extracted from the backlog line.
func TestParseBacklog_MetadataExtraction(t *testing.T) {
	tests := []struct {
		name              string
		input             string
		wantRepo          string
		wantKind          string
		wantSince         string
		wantBlocked       string
		wantTitleContains string
	}{
		{
			name:      "full metadata",
			input:     "- [ ] fc-test - ship: test description (repo: famclaw) (kind: ship) (since 2026-07-31)",
			wantRepo:  "famclaw",
			wantKind:  "ship",
			wantSince: "2026-07-31",
		},
		{
			name:        "blocked by",
			input:       "- [ ] fc-test - ship: test (repo: famclaw) (kind: ship) (since 2026-07-30) blocked-by: fc-childapprove-t15",
			wantRepo:    "famclaw",
			wantKind:    "ship",
			wantSince:   "2026-07-30",
			wantBlocked: "fc-childapprove-t15",
		},
		{
			name:              "no metadata",
			input:             "- [ ] fc-test - just a title",
			wantTitleContains: "just a title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bl, err := ParseBacklog("## In flight\n" + tt.input + "\n")
			if err != nil {
				t.Fatalf("ParseBacklog error: %v", err)
			}
			if len(bl.InFlight) != 1 {
				t.Fatalf("expected 1 in-flight item, got %d", len(bl.InFlight))
			}
			item := bl.InFlight[0]
			if tt.wantRepo != "" && item.Repo != tt.wantRepo {
				t.Errorf("Repo: got %q, want %q", item.Repo, tt.wantRepo)
			}
			if tt.wantKind != "" && item.Kind != tt.wantKind {
				t.Errorf("Kind: got %q, want %q", item.Kind, tt.wantKind)
			}
			if tt.wantSince != "" && item.Since != tt.wantSince {
				t.Errorf("Since: got %q, want %q", item.Since, tt.wantSince)
			}
			if tt.wantBlocked != "" && item.Blocked != tt.wantBlocked {
				t.Errorf("Blocked: got %q, want %q", item.Blocked, tt.wantBlocked)
			}
			if tt.wantTitleContains != "" && !strings.Contains(item.Title, tt.wantTitleContains) {
				t.Errorf("Title %q does not contain %q", item.Title, tt.wantTitleContains)
			}
		})
	}
}

// TestParseBacklog_DoneNotIncluded verifies that items in the "## Done"
// section are NOT parsed (read-only view of current work only).
func TestParseBacklog_DoneNotIncluded(t *testing.T) {
	input := `## In flight
- [ ] fc-live - live item (repo: famclaw)

## Queued
- [ ] fc-queued - queued item

## Done
- [x] fc-done - done item (repo: famclaw)
`
	bl, err := ParseBacklog(input)
	if err != nil {
		t.Fatalf("ParseBacklog error: %v", err)
	}
	for _, item := range bl.InFlight {
		if item.ID == "fc-done" {
			t.Error("Done section item should not appear in InFlight")
		}
	}
	for _, item := range bl.Queued {
		if item.ID == "fc-done" {
			t.Error("Done section item should not appear in Queued")
		}
	}
}

// TestParseBacklog_CheckboxStates verifies that only "- [ ]" (unchecked) lines
// are parsed, not "- [x]" (checked/done) lines.
func TestParseBacklog_CheckboxStates(t *testing.T) {
	input := `## In flight
- [ ] fc-pending - pending item
- [x] fc-done-but-in-flight - done checkbox in in-flight section

## Queued
- [ ] fc-queued - queued item
- [x] fc-done-in-queued - done checkbox in queued section
`
	bl, err := ParseBacklog(input)
	if err != nil {
		t.Fatalf("ParseBacklog error: %v", err)
	}
	// Only unchecked items should be parsed.
	if len(bl.InFlight) != 1 {
		t.Errorf("expected 1 in-flight item (only unchecked), got %d", len(bl.InFlight))
	}
	if len(bl.Queued) != 1 {
		t.Errorf("expected 1 queued item (only unchecked), got %d", len(bl.Queued))
	}
}

// TestBacklogFormatMarkdown verifies the Markdown output is well-formed.
func TestBacklogFormatMarkdown(t *testing.T) {
	tests := []struct {
		name string
		bl   *Backlog
		want []string
	}{
		{
			name: "empty",
			bl:   &Backlog{},
			want: []string{"In Flight", "Queued", "No in-flight", "No queued"},
		},
		{
			name: "with items",
			bl: &Backlog{
				InFlight: []BacklogItem{
					{ID: "fc-test", Title: "test description", Repo: "famclaw", Kind: "ship"},
				},
				Queued: []BacklogItem{
					{ID: "fc-queue", Title: "queued item"},
				},
			},
			want: []string{"In Flight", "Queued", "fc-test", "test description", "fc-queue", "queued item"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := tt.bl.FormatMarkdown()
			for _, sub := range tt.want {
				if !strings.Contains(output, sub) {
					t.Errorf("FormatMarkdown output missing %q: %q", sub, output)
				}
			}
		})
	}
}
