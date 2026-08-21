# Releases

How FamClaw releases are built, signed, and published — and the release-day
checklist.

## What the release pipeline does

Pushing a `v*` tag runs `.github/workflows/release.yml`:

1. **goreleaser** (macos-latest) — builds all targets (`CGO_ENABLED=0`),
   archives them, and handles darwin signing per the credential state
   (`scripts/darwin-sign-notarize.sh`, run from the goreleaser post-build
   hook): with credentials, every darwin binary is signed with a Developer ID
   Application certificate, submitted to Apple's notary service, and
   stapled; without them, the hook warns and falls back to ad-hoc signing
   ("Darwin signing: two paths" below). Cosign signs the checksums, syft
   emits SBOMs, and the build is attested.
2. **sd-images** (ubuntu-latest) — packages the linux binaries into
   flashable Raspberry Pi SD images.
3. **post-release** (ubuntu-latest) — downloads the published assets and
   verifies the cosign bundle, checksums, binary version, build attestation,
   and runs a server smoke test.

### Darwin signing: two paths

The darwin signing gate is **conditional on credentials being present**:

- **Credentials complete** (`FAMCLAW_APPLE_P12` + `_PASSPHRASE` +
  `_TEAM_ID`, plus either the notary-key triple or the Apple-ID pair):
  every darwin binary is Developer ID-signed (hardened runtime, RFC3161
  timestamp), notarized, and the Apple ticket is stapled *before
  archiving*, so the published tarballs contain the final signed binary. No
  release ever ships a darwin binary that Apple would refuse.
- **Credentials missing or partial**: the preflight step and the post-build
  hook print a WARNING naming each missing `FAMCLAW_*` secret, and the
  release proceeds with the pre-v0.13.0 fallback — the darwin binaries ship
  **ad-hoc-signed**. What that means for users:
  - the binaries **run on Apple Silicon out of the box**: ad-hoc signing
    satisfies the kernel's signature requirement, so there is no exit 137
    (#311 applies only to *unsigned* binaries);
  - a **browser-downloaded binary is blocked by Gatekeeper on first run**
    (#214). The workaround is right-click the binary → **Open** (then
    "Open" again in the dialog), or
    `xattr -d com.apple.quarantine <binary>`.
  - `scripts/update.sh` is unaffected: it installs via a checksum-verified
    curl download + atomic rename and never goes through Gatekeeper's
    browser-download path, so the updater works identically for both the
    ad-hoc and the Developer ID-signed binaries.

Add the credentials (below) to move future releases onto the signed path; an
ad-hoc fallback release is a valid, supported release in the meantime.

## Signing credentials (injected at release time)

Set these as **GitHub repository secrets** (the workflow maps them to
environment variables of the same names). Never commit credential material to
this repo.

| Secret | Required | Purpose |
|---|---|---|
| `FAMCLAW_APPLE_P12` | yes, for the signed path | base64 `.p12` containing the "Developer ID Application" certificate **and** its private key |
| `FAMCLAW_APPLE_PASSPHRASE` | yes, for the signed path | passphrase the `.p12` was exported with |
| `FAMCLAW_APPLE_TEAM_ID` | yes, for the signed path | Apple developer team ID |
| `FAMCLAW_NOTARY_KEY` | one of the two notarization options below | base64 App-Services notarytool API key `.p12` |
| `FAMCLAW_NOTARY_KEY_ID` | with `FAMCLAW_NOTARY_KEY` | key ID of that notary key |
| `FAMCLAW_NOTARY_ISSUER_ID` | with `FAMCLAW_NOTARY_KEY` | App-Services issuer ID |
| `FAMCLAW_APPLE_ID` | one of the two notarization options | Apple ID (alternative to the notary key) |
| `FAMCLAW_APPLE_PASSWORD` | with `FAMCLAW_APPLE_ID` | [app-specific password](https://appleid.apple.com) |

The notary key is preferred: it does not trigger Apple 2FA on release day.

All of the secrets above are optional: a release without them — or with a
partial set — still succeeds and ships ad-hoc-signed darwin binaries
(see "Darwin signing: two paths" above).

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
2. **Credentials — pick the path.** Confirm the repo secrets from the table
   are set for the signed path (`FAMCLAW_APPLE_P12`,
   `FAMCLAW_APPLE_PASSPHRASE`, `FAMCLAW_APPLE_TEAM_ID`, and either the
   notary-key triple or `FAMCLAW_APPLE_ID` + `FAMCLAW_APPLE_PASSWORD`).
   Without them the release still succeeds, but the darwin binaries ship
   ad-hoc-signed ("Darwin signing: two paths").
3. **Cut and push the tag:**
   ```sh
   git tag v0.13.0
   git push origin v0.13.0
   ```
4. **Watch the release workflow:**
   - `Preflight: darwin signing credentials` passes — it prints
     `Darwin signing credentials present.` on the signed path, or a WARNING
     naming the missing secrets on the ad-hoc fallback (both exit 0; the
     step never blocks the release on missing credentials),
   - the goreleaser log shows `Signing … / Notarizing … / Stapling …` per
     darwin binary and ends `OK: … Developer-ID signed, notarized, and
     stapled` (signed path) — or `WARNING: … AD-HOC SIGNED …`,
     `Ad-hoc signing …`, and `OK: … ad-hoc signed (no notarization)`
     (fallback),
   - sd-images and post-release verification pass.
5. **Verify the published darwin binary on an Apple Silicon Mac** (the real
   acceptance test for #311/#214):
   ```sh
   cd /tmp && rm -rf fc-verify && mkdir fc-verify && cd fc-verify
   curl -fsSLO https://github.com/famclaw/famclaw/releases/latest/download/famclaw-darwin-arm64.tar.xz
   tar -xJf famclaw-darwin-arm64.tar.xz
   codesign -dv famclaw            # signed: Authority = "Developer ID Application: <you>"
                                   # fallback: Authority = adhoc
   xcrun stapler verify famclaw    # signed: "… has a valid ticket / verified"
   ./famclaw --version             # exit 0 — NOT 137 (both paths)
   ```
   Then download the same tarball in a **browser** (so it carries
   `com.apple.quarantine`) and run it. On the signed path Gatekeeper must
   not block it. On the ad-hoc fallback path Gatekeeper blocks the first
   run by design — verify the workaround instead: right-click → Open (→
   Open again in the dialog).
6. **Verify integrity of all assets:** `sha256sum -c checksums.txt`, the cosign
   bundle, and `gh attestation verify` — exact commands are in the
   post-release job summary.
7. **Close the darwin issues with this evidence:** #311 (exit-137 — the
   `./famclaw --version` check passes on both paths) and #214 (browser
   download — the notarized install path on signed releases; on ad-hoc
   fallback releases, the right-click → Open workaround instead).

## Troubleshooting

- **Preflight prints a WARNING about darwin credentials.** Expected when
  the `FAMCLAW_*` secrets are absent or partial — the release proceeds with
  ad-hoc-signed darwin binaries ("Darwin signing: two paths"). To ship the
  signed path instead, set the listed secrets and re-run the release for the
  tag; no partial release exists because publishing happens only at the end.
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
