package familystate_test

import (
	"testing"

	"github.com/famclaw/famclaw/internal/familystate"
)

// TestProposal_RoundTrip encodes a Proposal to JSON then decodes it back,
// verifying all fields are preserved.
func TestProposal_RoundTrip(t *testing.T) {
	want := familystate.Proposal{
		Kind:       familystate.ProposalKind,
		Category:   "user_preferences",
		Subject:    "teo",
		Label:      "favorite_pizza",
		Value:      "pepperoni",
		Reason:     "Teo said so in chat",
		ProposedBy: "teo",
	}
	enc, err := familystate.EncodeProposal(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := familystate.DecodeProposal(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestDecodeProposal_WrongKind verifies that DecodeProposal rejects JSON whose
// kind field is not ProposalKind.
func TestDecodeProposal_WrongKind(t *testing.T) {
	enc := []byte(`{"kind":"content_approval","query":"hi"}`)
	_, err := familystate.DecodeProposal(enc)
	if err == nil {
		t.Error("DecodeProposal should reject non-family_fact_proposal kinds")
	}
}
