# Releases

How FamClaw releases are built, signed, and published — and the release-day
checklist.

## What the release pipeline does

Pushing a `v*` tag runs `.github/workflows/release.yml`:

1. **goreleaser** (macos-latest) — builds all targets (`CGO_ENABLED=0`),
   archives them, and signs every darwin binary with a Developer ID
   Application certificate, submits it to Apple's notary service, and staples
   the ticket (`scripts/darwin-sign-notarize.sh`, run from the goreleaser
   post-build hook). Cosign signs the checksums, syft emits SBOMs, and the
   build is attested.
2. **sd-images** (ubuntu-latest) — packages the linux binaries into
   flashable Raspberry Pi SD images.
3. **post-release** (ubuntu-latest) — downloads the published assets and
   verifies the cosign bundle, checksums, binary version, build attestation,
   and runs a server smoke test.

The darwin step is a **hard gate**: when signing credentials are missing the
job fails in the preflight step with an actionable message, *before anything
is built or published*. Rationale: an unsigned or ad-hoc signed arm64 binary
is killed on launch with a silent exit 137 (#311), and a non-notarized binary
is blocked by Gatekeeper after a browser download (#214). No release ever
ships a darwin binary that Apple would refuse.

## Signing credentials (injected at release time)

Set these as **GitHub repository secrets** (the workflow maps them to
environment variables of the same names). Never commit credential material to
this repo.

| Secret | Required | Purpose |
|---|---|---|
| `FAMCLAW_APPLE_P12` | yes | base64 `.p12` containing the "Developer ID Application" certificate **and** its private key |
| `FAMCLAW_APPLE_PASSPHRASE` | yes | passphrase the `.p12` was exported with |
| `FAMCLAW_APPLE_TEAM_ID` | yes | Apple developer team ID |
| `FAMCLAW_NOTARY_KEY` | one of the two notarization options below | base64 App-Services notarytool API key `.p12` |
| `FAMCLAW_NOTARY_KEY_ID` | with `FAMCLAW_NOTARY_KEY` | key ID of that notary key |
| `FAMCLAW_NOTARY_ISSUER_ID` | with `FAMCLAW_NOTARY_KEY` | App-Services issuer ID |
| `FAMCLAW_APPLE_ID` | one of the two notarization options | Apple ID (alternative to the notary key) |
| `FAMCLAW_APPLE_PASSWORD` | with `FAMCLAW_APPLE_ID` | [app-specific password](https://appleid.apple.com) |

The notary key is preferred: it does not trigger Apple 2FA on release day.

## One-time credential setup

1. **Developer ID Application certificate.** On a Mac: Apple Developer →
   Certificates, Identifiers & Profiles → Certificates → `+` → *Developer ID
   Application* (follow the assistant; the key lands in the login keychain).
2. **Export a `.p12`.** In Keychain Access, select the *certificate and its
   private key* (`Developer ID Application: <you>`), `File → Export…`, format
   *PKCS #12*. Set a passphrase. (Only the certificate + key — a `.cer`
   download alone has no private key and cannot sign.) If you have a `.cer`
   and a `.key` separately, combine them:
   `openssl pkcs12 -export -out identity.p12 -inkey key.pem -in cert.pem`.
   Then `base64 -i identity.p12 -o identity.p12.b64` (macOS) or
   `base64 -w0 identity.p12 > identity.p12.b64` (Linux).
3. **Notarization credentials — pick one:**
   - *App-Services API key (preferred):* developer.apple.com → App Services
     → Integrations → Keys → `+`, capability *App Store Connect* → *Notary
     Service*. Download the key, `base64` it, and note the key ID and issuer
     ID.
   - *Apple ID + app-specific password:* appleid.apple.com → Sign In and
     Security → App-Specific Passwords → generate one.
4. **GitHub secrets.** Repository → Settings → Secrets and variables →
   Actions → add every value from the table above. The secret name is the
   environment variable name the pipeline reads.
5. **Team ID.** developer.apple.com → account → Membership — the team ID
   (`T…` or an uppercase alphanumeric code).

## Release-day checklist

1. **Docs & changelog.** On `main`: the `CHANGELOG.md` section for this
   release exists; if the tag lands later than the draft date, update the
   section date to the tag day. README quick-start and `docs/` reflect the
   shipped features.
2. **Credentials.** Confirm the repo secrets from the table are set
   (`FAMCLAW_APPLE_P12`, `FAMCLAW_APPLE_PASSPHRASE`,
   `FAMCLAW_APPLE_TEAM_ID`, and either the notary-key triple or
   `FAMCLAW_APPLE_ID` + `FAMCLAW_APPLE_PASSWORD`).
3. **Cut and push the tag:**
   ```sh
   git tag v0.13.0
   git push origin v0.13.0
   ```
4. **Watch the release workflow:**
   - `Preflight: darwin signing credentials present` passes (on failure it
     lists the missing secrets — set them and *Re-run failed jobs*; nothing
     is published until the whole pipeline passes),
   - the goreleaser log shows `Signing … / Notarizing … / Stapling …` per
     darwin binary and ends `OK: … Developer-ID signed, notarized, and
     stapled`,
   - sd-images and post-release verification pass.
5. **Verify the published darwin binary on an Apple Silicon Mac** (the real
   acceptance test for #311/#214):
   ```sh
   cd /tmp && rm -rf fc-verify && mkdir fc-verify && cd fc-verify
   curl -fsSLO https://github.com/famclaw/famclaw/releases/latest/download/famclaw-darwin-arm64.tar.xz
   tar -xJf famclaw-darwin-arm64.tar.xz
   codesign -dv famclaw            # Authority should be "Developer ID Application: <you>"
   xcrun stapler verify famclaw    # "… has a valid ticket / verified"
   ./famclaw --version             # exit 0 — NOT 137
   ```
   Then download the same tarball in a **browser** (so it carries
   `com.apple.quarantine`) and run it: Gatekeeper must not block it.
6. **Verify integrity of all assets:** `sha256sum -c checksums.txt`, the cosign
   bundle, and `gh attestation verify` — exact commands are in the
   post-release job summary.
7. **Close the darwin issues with this evidence:** #311 (exit-137 fixed by
   signing) and #214 (notarized install path works for a browser download).

## Troubleshooting

- **Preflight fails.** The message lists the missing secrets. Set them, then
  *Re-run failed jobs* on the same tag — the tag push is already recorded,
  and no partial release exists because publishing happens only at the end.
- **Notarization rejected.** notarytool prints a log request ID; fetch the
  report with `xcrun notarytool log <request-id> --team-id <TEAM>`. Common
  causes: the binary changed after signing (it does not — the hook signs,
  then archives), or the certificate is not a *Developer ID Application*
  identity.
- **`codesign`/import errors on the runner.** `FAMCLAW_APPLE_P12` is not a
  valid base64 `.p12` (re-export: certificate + private key, PKCS #12).
- **User reports exit 137.** Two known causes, both documented in
  `docs/TROUBLESHOOTING.md`: an in-place overwrite during upgrade (the
  updater/installers use atomic rename — tell the user to re-run
  `scripts/update.sh`) and a genuine signing problem (probe with
  `codesign -dv` / `xcrun stapler verify`).
# no-op
