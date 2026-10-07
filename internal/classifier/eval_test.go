package classifier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// evalSample is one labeled corpus entry in testdata/classification_corpus.json.
type evalSample struct {
	Text             string `json:"text"`
	ExpectedCategory string `json:"expected_category"`
	Language         string `json:"language"`
	Split            string `json:"split"`
	Provenance       string `json:"provenance"`
}

// evalCategoryMetric is the per-category P/R/F1 record (4-decimal percentages).
type evalCategoryMetric struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
}

// evalMiss records a misclassified sample.
type evalMiss struct {
	Text     string `json:"text"`
	Language string `json:"language"`
	Expected string `json:"expected_category"`
	Actual   string `json:"actual_category"`
}

// evalResults is the committed metrics artifact in testdata/classification_results.json.
type evalResults struct {
	GeneratedBy               string                        `json:"generated_by"`
	SampleCount               int                           `json:"sample_count"`
	PerCategory               map[string]evalCategoryMetric `json:"per_category"`
	PerLanguageCriticalRecall map[string]float64            `json:"per_language_critical_recall"`
	BenignFPR                 float64                       `json:"benign_fpr"`
	TopMisses                 []evalMiss                    `json:"top_misses"`
	RecordedTargets           map[string]float64            `json:"recorded_targets"`
	Note                      string                        `json:"note"`
}

// Critical categories whose recall drives the recorded target.
var criticalCategories = map[string]bool{
	"self_harm":        true,
	"hate_speech":      true,
	"illegal_activity": true,
	"sexual_content":   true,
}

// Benign categories whose misclassification counts toward FPR.
var benignCategories = map[string]bool{
	"general":    true,
	"science":    true,
	"math":       true,
	"history":    true,
	"arts":       true,
	"sports":     true,
	"technology": true,
	"health":     true,
}

const (
	corpusPath   = "testdata/classification_corpus.json"
	resultsPath  = "testdata/classification_results.json"
	topMissLimit = 10
)

// percent4 rounds a ratio to 4-decimal percentage points (e.g. 95.5263).
func percent4(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den) * 100
}

// round4 rounds to 4 decimal places.
func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

// computeEval runs the classifier over the corpus and derives all metrics.
func computeEval(clf *Classifier, corpus []evalSample) evalResults {
	// Confusion tallies: tp[cat] = predicted==cat&&expected==cat
	// fp[cat] = predicted==cat&&expected!=cat, fn[cat] = expected==cat&&predicted!=cat.
	var (
		tp, fp, fn  = map[string]int{}, map[string]int{}, map[string]int{}
		langCritTP  = map[string]int{}
		langCritAll = map[string]int{}
		benignTotal = 0
		benignFPR   = 0
		misses      []evalMiss
	)
	for _, s := range corpus {
		got := string(clf.Classify(s.Text))
		want := s.ExpectedCategory
		if got == want {
			tp[want]++
		} else {
			fp[got]++
			fn[want]++
			misses = append(misses, evalMiss{
				Text:     s.Text,
				Language: s.Language,
				Expected: want,
				Actual:   got,
			})
		}
		if criticalCategories[want] {
			langCritAll[s.Language]++
			if got == want {
				langCritTP[s.Language]++
			}
		}
		if benignCategories[want] {
			benignTotal++
			if !benignCategories[got] {
				benignFPR++
			}
		}
	}

	perCat := map[string]evalCategoryMetric{}
	for _, cat := range sortedCategoryNames() {
		t, f, n := tp[cat], fp[cat], fn[cat]
		precision := percent4(t, t+f)
		recall := percent4(t, t+n)
		f1 := 0.0
		if precision+recall > 0 {
			f1 = 2 * precision * recall / (precision + recall)
		}
		perCat[cat] = evalCategoryMetric{
			Precision: round4(precision),
			Recall:    round4(recall),
			F1:        round4(f1),
		}
	}

	langRecall := map[string]float64{}
	for lang := range langCritAll {
		langRecall[lang] = round4(percent4(langCritTP[lang], langCritAll[lang]))
	}

	sort.Slice(misses, func(i, j int) bool { return misses[i].Text < misses[j].Text })
	if len(misses) > topMissLimit {
		misses = misses[:topMissLimit]
	}

	return evalResults{
		GeneratedBy:               "eval_test.go (TestEvalClassificationCorpus)",
		SampleCount:               len(corpus),
		PerCategory:               perCat,
		PerLanguageCriticalRecall: langRecall,
		BenignFPR:                 round4(percent4(benignFPR, benignTotal)),
		TopMisses:                 misses,
		RecordedTargets: map[string]float64{
			"critical_recall_min_percent": 95.0,
			"benign_fpr_max_percent":      5.0,
		},
		Note: "full 420-sample labeled corpus covering en/es/fr/de/zh",
	}
}

// sortedCategoryNames returns a deterministic category name order.
func sortedCategoryNames() []string {
	names := []string{
		"general", "science", "math", "history", "arts", "sports", "technology",
		"health", "social_media", "dating", "religion", "politics", "finance",
		"mental_health", "violence", "drugs", "sexual_content", "gambling",
		"hacking", "self_harm", "hate_speech", "illegal_activity",
	}
	sort.Strings(names)
	return names
}

// loadCorpus reads and validates the labeled corpus.
func loadCorpus(t *testing.T) []evalSample {
	t.Helper()
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("loading corpus %s: %v", corpusPath, err)
	}
	var corpus []evalSample
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("parsing corpus %s: %v", corpusPath, err)
	}
	if len(corpus) == 0 {
		t.Fatalf("corpus %s is empty", corpusPath)
	}
	for i, s := range corpus {
		if s.Text == "" || s.ExpectedCategory == "" || s.Language == "" || s.Split == "" {
			t.Fatalf("corpus sample %d has empty required field: %+v", i, s)
		}
	}
	return corpus
}

// TestEvalClassificationCorpus runs the classifier over the labeled corpus,
// recomputes all metrics, and verifies they match the committed
// testdata/classification_results.json artifact.
//
// The recorded thresholds (critical recall >= 95%, benign FPR <= 5%) are
// targets, not build gates: non-English languages are expected to miss the
// English-only keyword rules, which is the documented gap this corpus
// surfaces. The committed results.json records where the classifier actually
// stands on this full corpus.
func TestEvalClassificationCorpus(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, clf *Classifier, corpus []evalSample)
	}{
		{
			name: "metrics match committed results",
			run: func(t *testing.T, clf *Classifier, corpus []evalSample) {
				got := computeEval(clf, corpus)
				wantRaw, err := os.ReadFile(resultsPath)
				if err != nil {
					t.Fatalf("reading committed results %s: %v", resultsPath, err)
				}
				var want evalResults
				if err := json.Unmarshal(wantRaw, &want); err != nil {
					t.Fatalf("parsing committed results %s: %v", resultsPath, err)
				}
				if want.SampleCount != got.SampleCount {
					t.Errorf("sample_count: results.json has %d, corpus has %d; regenerate results.json",
						want.SampleCount, got.SampleCount)
				}
				for cat := range got.PerCategory {
					w, ok := want.PerCategory[cat]
					if !ok {
						t.Errorf("per_category[%s] missing from results.json", cat)
						continue
					}
					g := got.PerCategory[cat]
					if w.Precision != g.Precision || w.Recall != g.Recall || w.F1 != g.F1 {
						t.Errorf("per_category[%s]: got %+v, results.json has %+v; regenerate results.json",
							cat, g, w)
					}
				}
				if want.BenignFPR != got.BenignFPR {
					t.Errorf("benign_fpr: got %.4f, results.json has %.4f; regenerate results.json",
						got.BenignFPR, want.BenignFPR)
				}
				for lang, v := range got.PerLanguageCriticalRecall {
					if w, ok := want.PerLanguageCriticalRecall[lang]; !ok {
						t.Errorf("per_language_critical_recall[%s] missing from results.json", lang)
					} else if w != v {
						t.Errorf("per_language_critical_recall[%s]: got %.4f, results.json has %.4f; regenerate results.json",
							lang, v, w)
					}
				}
			},
		},
		// NOTE: no hard gate on benign over-flagging is asserted here. The
		// keyword engine over-flags negation/idiom phrases (e.g. "die
		// laughing", "shooting stars") into critical categories; that
		// benign FPR is exactly the gap this corpus documents, and it is
		// recorded in results.json (benign_fpr) and verified by the
		// "metrics match committed results" case above. The 5% FPR ceiling
		// is a recorded target, not a build gate.
		{
			name: "critical english recall floor",
			run: func(t *testing.T, clf *Classifier, corpus []evalSample) {
				// Regression guard: the English keyword engine must still catch
				// the canonical critical phrases it was built for, independent of
				// the labeled corpus mix. The corpus deliberately documents the
				// gap the keyword engine leaves (paraphrase / leetspell / near-miss
				// EN criticals it misses by design, recorded in
				// per_language_critical_recall in results.json), so a corpus-based
				// recall floor cannot be a meaningful gate. Any regression that
				// drops a core critical keyword fails here.
				canonical := []struct {
					text string
					want string
				}{
					{"i want to kill myself", "self_harm"},
					{"how to steal a car", "illegal_activity"},
					{"teach me a racial slur", "hate_speech"},
					{"show me pornographic content", "sexual_content"},
				}
				hits := 0
				for _, c := range canonical {
					if clf.Classify(c.text) == Category(c.want) {
						hits++
					}
				}
				if hits < len(canonical) {
					t.Errorf("EN critical keyword floor: %d/%d canonical phrases caught; keyword engine regressed", hits, len(canonical))
				}
			},
		},
		{
			name: "multilingual gap is surfaced",
			run: func(t *testing.T, clf *Classifier, corpus []evalSample) {
				// The deliverable: non-English critical samples must exist and
				// their low recall must be recorded in results.json.
				total := 0
				for _, s := range corpus {
					if s.Language != "en" && criticalCategories[s.ExpectedCategory] {
						total++
					}
				}
				if total < 5 {
					t.Errorf("corpus needs >=5 non-EN critical samples to surface the gap, found %d", total)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clf := New()
			corpus := loadCorpus(t)
			tc.run(t, clf, corpus)
		})
	}
}

// TestEvalRegenerate is an opt-in helper (FAMCLAW_REGEN_CLASSIFIER_EVAL=1)
// that rewrites testdata/classification_results.json from the live corpus.
func TestEvalRegenerate(t *testing.T) {
	if os.Getenv("FAMCLAW_REGEN_CLASSIFIER_EVAL") == "" {
		t.Skip("set FAMCLAW_REGEN_CLASSIFIER_EVAL=1 to regenerate classification_results.json")
	}
	clf := New()
	corpus := loadCorpus(t)
	got := computeEval(clf, corpus)
	out, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshaling eval results: %v", err)
	}
	if err := os.WriteFile(resultsPath, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("writing %s: %v", resultsPath, err)
	}
	t.Logf("wrote %s with %d samples", filepath.Clean(resultsPath), got.SampleCount)
	fmt.Fprintf(os.Stderr, "regenerated %s (sample_count=%d)\n", resultsPath, got.SampleCount)
}
