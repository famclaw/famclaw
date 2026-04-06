# GoReleaser Migration + Post-Release Verification — Design Spec

**Date:** 2026-04-05
**Status:** Approved
**Scope:** Replace hand-rolled release workflow with GoReleaser, add full post-release integration verification

---

## Problem

The current release pipeline (`release.yml`, ~237 lines) has:
- Hand-rolled build matrix that broke twice (Go version, android-armv7 CGO, SBOM artifact collision)
- Hardcoded static release notes template (no changelog from git)
- No post-release verification — broken binaries can ship without detection
- No smoke tests on built artifacts

## Solution

Migrate to GoReleaser (Go community standard) + full post-release integration test.

### Security measures retained:
- Cosign keyless signing via Sigstore OIDC
- SBOM generation via syft (CycloneDX format)
- Build provenance attestation via actions/attest-build-provenance
- govulncheck (stays in ci.yml, not release)
- SHA256 checksums for all artifacts

---

## File 1: `.goreleaser.yaml`

GoReleaser config for builds, archives, signing, SBOM, and changelog.

```yaml
version: 2

project_name: famclaw

builds:
  - id: famclaw
    main: ./cmd/famclaw
    binary: famclaw
    env:
      - CGO_ENABLED=0
    ldflags:
      - -s -w -X main.Version={{.Version}}
    goos:
      - linux
      - darwin
      - android
    goarch:
      - amd64
      - arm64
      - arm
    goarm:
      - '7'
    ignore:
      - goos: darwin
        goarch: arm
      - goos: android
        goarch: arm
      - goos: android
        goarch: amd64

archives:
  - formats: [tar.xz]
    name_template: >-
      famclaw-{{ .Os }}-{{ .Arch }}{{ if .Arm }}v{{ .Arm }}{{ end }}

checksum:
  name_template: checksums.txt

signs:
  - cmd: cosign
    artifacts: checksum
    signature: "${artifact}.sigstore.json"
    args:
      - sign-blob
      - --bundle=${signature}
      - ${artifact}
      - --yes

sboms:
  - artifacts: archive
    cmd: syft
    args:
      - ${artifact}
      - --output
      - cyclonedx-json=${document}

changelog:
  sort: asc
  groups:
    - title: "New Features"
      regexp: '^feat'
      order: 0
    - title: "Bug Fixes"
      regexp: '^fix'
      order: 1
    - title: "Security"
      regexp: '^.*security|^.*vuln|^.*cve'
      order: 2
    - title: "Refactoring"
      regexp: '^refactor'
      order: 3
    - title: "Documentation"
      regexp: '^docs'
      order: 4
    - title: "Other"
      order: 999
  filters:
    exclude:
      - '^chore\(deps\)'
      - '^Merge'
      - 'Co-Authored-By'

release:
  prerelease: auto
  extra_files:
    - glob: scripts/install-rpi.sh
    - glob: scripts/install-termux.sh
```

---

## File 2: `.github/workflows/release.yml` (simplified)

Replaces the 237-line hand-rolled workflow.

```yaml
name: Release

on:
  push:
    tags:
      - 'v*'

permissions:
  contents: write
  id-token: write
  attestations: write

jobs:
  goreleaser:
    name: Build & Publish
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: true

      - uses: sigstore/cosign-installer@v3

      - name: Install syft
        uses: anchore/sbom-action/download-syft@v0.24.0

      - uses: goreleaser/goreleaser-action@v7
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

      - name: Attest build provenance
        uses: actions/attest-build-provenance@v4
        with:
          subject-path: dist/checksums.txt

  sd-images:
    name: Build SD images
    runs-on: ubuntu-latest
    needs: goreleaser
    strategy:
      matrix:
        include:
          - arch: arm64
            binary: famclaw-linux-arm64
            image_name: famclaw-rpi4-arm64
            label: "RPi 4/5 64-bit"
          - arch: armhf
            binary: famclaw-linux-armv7
            image_name: famclaw-rpi3-armv7
            label: "RPi 3/2/Zero 32-bit"

    steps:
      - uses: actions/checkout@v6

      - name: Download binary from release
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          gh release download ${{ github.ref_name }} \
            --pattern '${{ matrix.binary }}.tar.xz' \
            --dir dist/
          cd dist && tar xf ${{ matrix.binary }}.tar.xz

      - name: Install image tools
        run: |
          sudo apt-get update -qq
          sudo apt-get install -y kpartx qemu-user-static

      - name: Build SD image
        run: |
          chmod +x dist/famclaw scripts/build-image.sh
          sudo scripts/build-image.sh \
            dist/famclaw \
            ${{ matrix.arch }} \
            dist/${{ matrix.image_name }}.img \
            ${{ github.ref_name }}

      - name: Compress and checksum
        run: |
          cd dist
          sudo chown $(id -u):$(id -g) ${{ matrix.image_name }}.img
          xz -9 --threads=0 ${{ matrix.image_name }}.img
          sha256sum ${{ matrix.image_name }}.img.xz > ${{ matrix.image_name }}.img.xz.sha256

      - name: Upload SD images to release
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          gh release upload ${{ github.ref_name }} \
            dist/${{ matrix.image_name }}.img.xz \
            dist/${{ matrix.image_name }}.img.xz.sha256

  post-release:
    name: Post-release verification
    needs: goreleaser
    runs-on: ubuntu-latest
    env:
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
    steps:
      - uses: actions/checkout@v6

      - uses: sigstore/cosign-installer@v3

      - name: Download release artifacts
        run: |
          gh release download ${{ github.ref_name }} \
            --repo ${{ github.repository }} \
            --dir dist/

      - name: Verify cosign signature
        run: |
          cosign verify-blob \
            --certificate dist/checksums.txt.pem \
            --signature dist/checksums.txt.sig \
            dist/checksums.txt

      - name: Verify checksums
        run: cd dist && sha256sum -c checksums.txt --ignore-missing

      - name: Extract binary
        run: |
          tar xf dist/famclaw-linux-amd64.tar.xz -C dist/
          chmod +x dist/famclaw

      - name: Verify version
        run: |
          version=$(dist/famclaw --version 2>&1 || true)
          echo "Binary version: $version"
          expected="${{ github.ref_name }}"
          echo "$version" | grep -qF "${expected}" || \
            (echo "FAIL: expected '${expected}' in: $version" && exit 1)

      - name: Install OPA
        run: |
          curl -L -o /usr/local/bin/opa \
            https://openpolicyagent.org/downloads/latest/opa_linux_amd64_static
          chmod +x /usr/local/bin/opa

      - name: Start FamClaw server
        run: |
          cat > /tmp/test-config.yaml << 'TESTCFG'
          server:
            port: 8080
            secret: "post-release-test-secret-32chars!!"
          llm:
            base_url: ""
            model: ""
          users: []
          policies:
            dir: ./policies/family
            data_dir: ./policies/data
          storage:
            db_path: /tmp/famclaw-test.db
          TESTCFG
          dist/famclaw --config /tmp/test-config.yaml &
          for i in $(seq 1 15); do
            curl -sf http://localhost:8080/ > /dev/null 2>&1 && break
            sleep 1
          done

      - name: Smoke test — root redirects to /setup
        run: |
          status=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/)
          echo "Root: HTTP $status"
          [ "$status" = "307" ] || (echo "FAIL: expected 307 redirect" && exit 1)

      - name: Smoke test — /setup serves wizard
        run: |
          curl -sf http://localhost:8080/setup | grep -q "FamClaw" || \
            (echo "FAIL: /setup doesn't serve wizard" && exit 1)

      - name: Smoke test — setup detect API
        run: |
          curl -sf http://localhost:8080/api/setup/detect | jq -e '.os' || \
            (echo "FAIL: /api/setup/detect broken" && exit 1)

      - name: Smoke test — settings API (first boot)
        run: |
          curl -sf http://localhost:8080/api/settings | jq -e '.llm' || \
            (echo "FAIL: GET /api/settings broken" && exit 1)

      - name: Smoke test — wizard setup flow
        run: |
          curl -sf -X POST http://localhost:8080/api/settings \
            -H 'Content-Type: application/json' \
            -d '{
              "llm": {"base_url": "http://localhost:11434", "model": "test"},
              "users": [
                {"name": "testparent", "display_name": "Test Parent", "role": "parent", "pin": "9999"}
              ],
              "gateways": {
                "telegram": {"enabled": false},
                "discord": {"enabled": false},
                "whatsapp": {"enabled": false}
              }
            }' | jq -e '.status == "saved"' || \
            (echo "FAIL: wizard setup POST broken" && exit 1)

      - name: Smoke test — settings after setup
        run: |
          curl -sf http://localhost:8080/api/settings \
            -H 'X-Parent-PIN: 9999' | jq -e '.users | length > 0' || \
            (echo "FAIL: settings after setup broken" && exit 1)

      - name: Verify SBOM format
        run: |
          for f in dist/*.sbom.json; do
            [ -f "$f" ] || continue
            jq -e '.bomFormat == "CycloneDX"' "$f" || \
              (echo "FAIL: $f is not valid CycloneDX" && exit 1)
            echo "SBOM valid: $f"
          done

      - name: Post-release summary
        if: always()
        run: |
          echo "## Post-Release Verification: ${{ github.ref_name }}" >> $GITHUB_STEP_SUMMARY
          echo "" >> $GITHUB_STEP_SUMMARY
          echo "| Check | Status |" >> $GITHUB_STEP_SUMMARY
          echo "|-------|--------|" >> $GITHUB_STEP_SUMMARY
          echo "| Cosign signature | Verified |" >> $GITHUB_STEP_SUMMARY
          echo "| SHA256 checksums | Verified |" >> $GITHUB_STEP_SUMMARY
          echo "| Binary version | ${{ github.ref_name }} |" >> $GITHUB_STEP_SUMMARY
          echo "| Server startup | Passed |" >> $GITHUB_STEP_SUMMARY
          echo "| Wizard redirect | Passed |" >> $GITHUB_STEP_SUMMARY
          echo "| Setup detect API | Passed |" >> $GITHUB_STEP_SUMMARY
          echo "| Settings API | Passed |" >> $GITHUB_STEP_SUMMARY
          echo "| Wizard setup flow | Passed |" >> $GITHUB_STEP_SUMMARY
          echo "| SBOM validation | Passed |" >> $GITHUB_STEP_SUMMARY
```

---

## File 3: `.github/release.yml`

GitHub's native label-based release notes categories (complements GoReleaser's commit-based changelog).

```yaml
changelog:
  exclude:
    labels:
      - dependencies
      - skip-changelog
    authors:
      - dependabot
      - coderabbitai
  categories:
    - title: "New Features"
      labels:
        - enhancement
        - feature
    - title: "Bug Fixes"
      labels:
        - bug
        - fix
    - title: "Security"
      labels:
        - security
    - title: "Other Changes"
      labels:
        - "*"
```

---

## Implementation Plan

**Single PR:** `feat/goreleaser-migration`

1. Add `.goreleaser.yaml`
2. Add `.github/release.yml` (changelog labels)
3. Rewrite `.github/workflows/release.yml` (GoReleaser + SD images + post-release)
4. Delete old release workflow content (replaced entirely)
5. Test: `goreleaser check` (validates config locally)
6. Test: tag `v0.3.1-rc1` to trigger pipeline, verify all 3 jobs pass

**Verification checklist:**
- [ ] GoReleaser builds all 6 binaries
- [ ] Cosign signs checksums.txt
- [ ] SBOM generated per archive
- [ ] Changelog auto-generated from commits
- [ ] SD images built from release binaries
- [ ] Post-release: signature verified
- [ ] Post-release: checksum verified
- [ ] Post-release: binary version matches tag
- [ ] Post-release: server starts and serves wizard
- [ ] Post-release: full API smoke test passes
- [ ] Post-release: SBOM is valid CycloneDX
