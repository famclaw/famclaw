package webfilter

import "testing"

func TestBaselineBlocks_KnownHarmHosts(t *testing.T) {
	// Representative kid-harm entries that MUST remain in the baseline.
	blocked := []string{
		"omegle.com",
		"kiwifarms.net",
		"stormfront.org",
		"selfharm.net",
		"bestgore.com",
		"gambling.example.com", // NOT in list; used below as a negative via subdomain test
	}
	for _, h := range blocked[:5] {
		if !BaselineBlocks(h) {
			t.Errorf("BaselineBlocks(%q) = false, want true", h)
		}
	}
}

func TestBaselineBlocks_Subdomain(t *testing.T) {
	// A subdomain of a baseline host must match via suffix.
	if !BaselineBlocks("www.omegle.com") {
		t.Error("BaselineBlocks(wwww.omegle.com) = false, want true (subdomain suffix match)")
	}
	if !BaselineBlocks("sub.kiwifarms.net") {
		t.Error("BaselineBlocks(sub.kiwifarms.net) = false, want true (subdomain suffix match)")
	}
}

func TestBaselineBlocks_RemovedMainstreamNotBlocked(t *testing.T) {
	// Entries deliberately curated OUT (mainstream comm / social / news /
	// support) must NOT be in the baseline, so a family assistant can reach them.
	allowed := []string{
		"discord.com",
		"telegram.org",
		"rumble.com",
		"odysee.com",
		"truthsocial.com",
		"gettr.com",
		"infowars.com",
		"zerohedge.com",
		"edsupportforum.com",
	}
	for _, h := range allowed {
		if BaselineBlocks(h) {
			t.Errorf("BaselineBlocks(%q) = true, want false (curated out)", h)
		}
	}
}

func TestBaselineBlocks_NoLeakOfUnknown(t *testing.T) {
	if BaselineBlocks("") {
		t.Error("BaselineBlocks(\"\") should be false")
	}
	if BaselineBlocks("example.com") {
		t.Error("BaselineBlocks(example.com) should be false (not in baseline)")
	}
}

func TestBaselineCount_IsNonTrivial(t *testing.T) {
	n := BaselineCount()
	// The curated Aegis core is ~110+ hosts; guard against an accidental
	// empty/placeholder baseline (the failure mode that shipped .example.com).
	if n < 90 {
		t.Errorf("BaselineCount() = %d, want >= 90 (baseline looks empty/placeholder)", n)
	}
	// And it must not have accidentally bloated into a mega-list.
	if n > 300 {
		t.Errorf("BaselineCount() = %d, want <= 300 (baseline should stay small)", n)
	}
}
