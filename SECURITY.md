# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest  | Yes       |
| < latest | No       |

## Reporting a Vulnerability

If you discover a security vulnerability in FamClaw, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead:

1. **GitHub Security Advisories (preferred):** Go to [Security Advisories](https://github.com/famclaw/famclaw/security/advisories/new) and create a private advisory.
2. **Email:** Send details to the repository maintainers via the email listed in the GitHub org profile.

### What to include

- Description of the vulnerability
- Steps to reproduce
- Impact assessment
- Suggested fix (if any)

### Response timeline

- **Acknowledgment:** within 48 hours
- **Initial assessment:** within 7 days
- **Fix or mitigation:** within 30 days for critical issues

## Verify a Release

All release artifacts are signed and attested:

```bash
# Download checksums and sigstore bundle
gh release download v0.3.0-beta --pattern 'checksums*' --dir .

# Verify cosign signature (Sigstore keyless)
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp='github\.com/famclaw/famclaw' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  checksums.txt

# Verify checksum of your binary
sha256sum -c checksums.txt --ignore-missing

# Verify GitHub build attestation
gh attestation verify checksums.txt --repo famclaw/famclaw
```

## Security Measures

### Release pipeline
- **Cosign keyless signing** — checksums signed via Sigstore OIDC, logged to Rekor transparency log
- **SBOM** — CycloneDX via syft for every binary
- **Build provenance attestation** — GitHub-native, verifiable via `gh attestation verify`
- **SHA256 checksums** — all artifacts covered
- **Post-release smoke tests** — binary version, server startup, API integration, signature verification

### CI pipeline
- **govulncheck** — blocks merges on known Go CVEs
- **CodeQL** — semantic code analysis on every PR
- **gosec** — static security analysis
- **Dependency review** — blocks PRs with critical vulnerabilities
- **TruffleHog** — secret scanning via CodeRabbit
- **OpenSSF Scorecard** — supply chain security posture tracking

### Runtime
- **OPA policy engine** — all messages evaluated before reaching the LLM
- **Honeybadger** — runtime security scanning of skills/MCP tools
- **No CGO** — single static binary, no C dependencies, reduced attack surface
- **All GitHub Actions pinned to SHA** — prevents supply chain attacks via tag manipulation
