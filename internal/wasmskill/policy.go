package wasmskill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Execution tiers for the wasm-or-refuse policy.
const (
	// TierWasm is the only tier the host executes; it requires a valid
	// checksummed .wasm artifact and the full skill boundary.
	TierWasm = "wasm"
	// TierRefused is the verdict for every skill that is not a Wasm
	// artifact.
	TierRefused = "refused"
)

// wasmName matches skill names like "note_search.wasm" (an ASCII stem,
// then ".wasm").
var wasmName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-]*\.wasm$`)

// Policy is a verdict on whether a skill may run, and how.
type Policy struct {
	// Tier is TierWasm or TierRefused.
	Tier string
	// Reason explains the verdict (for refused skills, what was missing).
	Reason string
}

// SkillRef describes a skill candidate for the wasm-or-refuse policy. The
// live skill bridge has no artifact fields yet (a follow-up adds them to
// the skill manifest); P3 resolves a SkillRef from the skill registry.
type SkillRef struct {
	// Name is the skill name (must be a .wasm artifact name to run).
	Name string
	// Artifact is the filesystem path of the .wasm file.
	Artifact string
	// Checksum is the artifact's "sha256:<hex>" checksum.
	Checksum string
}

// Evaluate applies the wasm-or-refuse policy to a skill candidate: it runs
// only when it is a Wasm artifact with a well-formed name, a non-empty
// artifact path, and a sha256: checksum. Everything else is refused with a
// machine-readable reason.
func Evaluate(sk SkillRef) Policy {
	if !wasmName.MatchString(sk.Name) {
		return Policy{Tier: TierRefused, Reason: fmt.Sprintf("name %q is not a wasm artifact", sk.Name)}
	}
	if sk.Artifact == "" {
		return Policy{Tier: TierRefused, Reason: "missing artifact"}
	}
	if !strings.HasPrefix(sk.Checksum, "sha256:") {
		return Policy{Tier: TierRefused, Reason: "missing or malformed sha256 checksum"}
	}
	return Policy{Tier: TierWasm, Reason: "wasm artifact with valid name and checksum"}
}

// LoadWasmArtifact admits a wasm artifact per Evaluate, reads the file,
// and verifies its sha256 checksum before the runtime is allowed to
// compile it. A refused or mismatching artifact is never read.
func LoadWasmArtifact(sk SkillRef) ([]byte, error) {
	p := Evaluate(sk)
	if p.Tier != TierWasm {
		return nil, fmt.Errorf("wasmskill: refusing to load %q: %s", sk.Name, p.Reason)
	}
	b, err := os.ReadFile(sk.Artifact)
	if err != nil {
		return nil, fmt.Errorf("wasmskill: reading artifact %q: %w", sk.Artifact, err)
	}
	sum := sha256.Sum256(b)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if got != sk.Checksum {
		return nil, fmt.Errorf("wasmskill: checksum mismatch for %q (got %s)", sk.Artifact, got)
	}
	return b, nil
}
