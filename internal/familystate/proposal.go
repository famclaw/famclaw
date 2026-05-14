package familystate

import (
	"encoding/json"
	"fmt"
)

// ProposalKind is the discriminator stored in approvals.query_text JSON
// envelopes. Approvals with this kind are family_fact proposals; the
// approve_request handler dispatches on this value to call UpsertFact.
const ProposalKind = "family_fact_proposal"

// Proposal is the JSON envelope written into approvals.query_text when
// a child calls propose_family_fact. The existing approvals table stays
// as-is; only the payload schema is new (R3 council: no new table, just
// dispatch on kind in the existing approval handler).
type Proposal struct {
	Kind       string `json:"kind"` // must equal ProposalKind for decode to succeed
	Category   string `json:"category"`
	Subject    string `json:"subject"`
	Label      string `json:"label"`
	Value      string `json:"value"`
	Reason     string `json:"reason,omitempty"`
	ProposedBy string `json:"proposed_by"`
}

// EncodeProposal serializes a Proposal to the JSON form stored in
// approvals.query_text. Always sets Kind = ProposalKind.
func EncodeProposal(p Proposal) ([]byte, error) {
	p.Kind = ProposalKind
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode proposal: %w", err)
	}
	return b, nil
}

// DecodeProposal parses the JSON envelope. Returns an error if the
// envelope's kind != ProposalKind — callers should only invoke this on
// approvals rows that already carry the family_fact_proposal category
// marker, but this guard keeps the decoder honest.
func DecodeProposal(data []byte) (Proposal, error) {
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal: %w", err)
	}
	if p.Kind != ProposalKind {
		return Proposal{}, fmt.Errorf("decode proposal: wrong kind %q (want %q)", p.Kind, ProposalKind)
	}
	return p, nil
}
