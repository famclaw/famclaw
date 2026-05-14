package familystate

import (
	"testing"
)

func TestProposalEnvelope_RoundTrip(t *testing.T) {
	want := Proposal{
		Kind:       "family_fact_proposal",
		Category:   "user_preferences",
		Subject:    "teo",
		Label:      "favorite_pizza",
		Value:      "pepperoni",
		Reason:     "Teo said so in chat",
		ProposedBy: "teo",
	}
	enc, err := EncodeProposal(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeProposal(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestProposalEnvelope_RejectsWrongKind(t *testing.T) {
	enc := `{"kind":"content_approval","query":"hi"}`
	_, err := DecodeProposal([]byte(enc))
	if err == nil {
		t.Error("DecodeProposal should reject non-family_fact_proposal kinds")
	}
}
