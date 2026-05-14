package familystate

import (
	"encoding/json"
	"testing"
)

// TestProposalEnvelope_Encode covers what EncodeProposal must produce:
// always-stamped Kind, all fields surfaced in JSON, Reason omittable.
func TestProposalEnvelope_Encode(t *testing.T) {
	cases := []struct {
		name    string
		in      Proposal
		wantKey string // a substring that must appear in the JSON output
	}{
		{
			name:    "happy path: every field populated",
			in:      Proposal{Kind: "family_fact_proposal", Category: "user_preferences", Subject: "teo", Label: "favorite_pizza", Value: "pepperoni", Reason: "Teo said so", ProposedBy: "teo"},
			wantKey: `"reason":"Teo said so"`,
		},
		{
			name:    "no reason: reason key omitted (omitempty)",
			in:      Proposal{Category: "pets", Subject: "family", Label: "fish", Value: "goldfish", ProposedBy: "julia"},
			wantKey: `"label":"fish"`,
		},
		{
			name:    "caller-supplied wrong Kind is overwritten",
			in:      Proposal{Kind: "content_approval", Category: "x", Subject: "y", Label: "z", Value: "v", ProposedBy: "dep"},
			wantKey: `"kind":"family_fact_proposal"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := EncodeProposal(tc.in)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !contains(string(enc), tc.wantKey) {
				t.Errorf("encoded JSON missing %q:\n%s", tc.wantKey, string(enc))
			}
			if !contains(string(enc), `"kind":"family_fact_proposal"`) {
				t.Errorf("EncodeProposal did not stamp Kind: %s", string(enc))
			}
			// Round-trip every case through DecodeProposal — kind is
			// always family_fact_proposal after EncodeProposal, so
			// Decode must succeed.
			got, err := DecodeProposal(enc)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := tc.in
			want.Kind = ProposalKind
			if got != want {
				t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
			}
		})
	}
}

// TestProposalEnvelope_DecodeRejects covers every reason DecodeProposal
// must reject input — wrong kind, malformed JSON, missing kind.
func TestProposalEnvelope_DecodeRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{name: "different kind", input: `{"kind":"content_approval","query":"hi"}`},
		{name: "kind missing", input: `{"category":"pets","subject":"family"}`},
		{name: "empty kind", input: `{"kind":"","category":"x"}`},
		{name: "not json at all", input: `not json`},
		{name: "truncated", input: `{"kind":"family_fact_propos`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeProposal([]byte(tc.input))
			if err == nil {
				t.Errorf("DecodeProposal(%q) should have errored", tc.input)
			}
		})
	}
}

// contains is a small substring helper to keep the encode test readable.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Verify the encode result is valid JSON (defensive: catches mis-shapes
// like accidentally double-encoding).
func TestProposalEnvelope_EncodeProducesValidJSON(t *testing.T) {
	enc, err := EncodeProposal(Proposal{Category: "x", Subject: "y", Label: "z", Value: "v", ProposedBy: "dep"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(enc, &raw); err != nil {
		t.Errorf("not valid JSON: %v\n%s", err, string(enc))
	}
}
