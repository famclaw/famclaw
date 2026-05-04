package prompt

import (
	"os"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

func TestBuild_Snapshots(t *testing.T) {
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

	cases := []struct {
		name string
		file string
		ctx  BuildContext
	}{
		{"parent", "testdata/parent.snap", BuildContext{Cfg: cfg, User: parent, Gateway: "telegram", Skills: []string{"seccheck"}, OAuth: true, HardBlocked: []string{"weapons", "self_harm"}}},
		{"child_under_8", "testdata/child_under_8.snap", BuildContext{Cfg: cfg, User: teo, Gateway: "telegram", HardBlocked: []string{"weapons", "self_harm"}}},
		{"child_age_8_12", "testdata/child_age_8_12.snap", BuildContext{Cfg: cfg, User: julia, Gateway: "telegram", HardBlocked: []string{"weapons"}}},
		{"child_age_13_17", "testdata/child_age_13_17.snap", BuildContext{Cfg: cfg, User: sam, Gateway: "web"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := Build(tc.ctx)
			if os.Getenv("UPDATE_PROMPT_SNAPSHOTS") == "1" {
				if err := os.WriteFile(tc.file, []byte(actual), 0644); err != nil {
					t.Fatalf("write %s: %v", tc.file, err)
				}
				t.Logf("updated %s", tc.file)
				return
			}
			expected, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			if string(expected) != actual {
				t.Errorf("snapshot %s mismatch.\n--- expected ---\n%s\n--- actual ---\n%s\nIf intentional, regenerate with UPDATE_PROMPT_SNAPSHOTS=1.", tc.file, string(expected), actual)
			}
		})
	}
}
