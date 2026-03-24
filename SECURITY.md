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

## Security Measures

FamClaw implements the following security practices:

- **SLSA Build Provenance:** All release binaries include attestation (SLSA Build L3)
- **SBOM:** Software Bill of Materials attached to every release (CycloneDX format)
- **Dependency scanning:** Dependabot monitors Go modules and GitHub Actions for CVEs
- **Static analysis:** CodeQL and gosec run on every PR
- **Vulnerability checks:** `govulncheck` runs in CI and blocks merges on known CVEs
- **Secret scanning:** TruffleHog scans PRs for leaked credentials via CodeRabbit
- **Policy engine:** All user messages pass through OPA policy evaluation before reaching the LLM
- **No CGO:** Single static binary, no C dependencies, reduced attack surface
