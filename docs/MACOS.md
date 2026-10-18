# macOS (developers only)

FamClaw's supported end-user platform is the Raspberry Pi SD image
(`docs/HARDWARE.md`). macOS binaries are built and released, but they are
**developer-only**: they are ad-hoc-signed and **not notarized by Apple**.

**Project decision (#214):** the project does not hold — and does not plan to
purchase — an Apple Developer ID, so notarization is not on the roadmap. The
double-click / browser-download install path therefore does not exist for
macOS. If a non-technical family member needs FamClaw, flash the RPi image;
on a Mac, FamClaw is for developers who live in a terminal.

## Why Gatekeeper blocks a browser download

A browser adds the `com.apple.quarantine` extended attribute to everything it
downloads. macOS then refuses to run an un-notarized quarantined binary with
"cannot be opened because the developer cannot be verified". A `curl` (or
`scp`, or `git`) download does **not** set that attribute, which is why the
terminal path below works without any Gatekeeper workaround.

`scripts/update.sh` never goes through the browser path (checksum-verified
`curl` + atomic rename), so the updater is unaffected.

## Install from the terminal (supported path)

One command, from any clone of this repo or from the release asset of the
same name:

```sh
bash scripts/install-macos-dev.sh
```

The script detects Apple Silicon vs Intel, downloads the matching darwin
build, verifies it against the release `checksums.txt`, clears any quarantine
attribute, ad-hoc re-signs the binary locally, installs it under
`~/.local/bin`, and prints the version it installed. Set `FAMCLAW_VERSION` to
pin a specific tag; set `FAMCLAW_BIN` to choose the install path.

Then, in the directory where you want your data:

```sh
mkdir -p ~/famclaw && cd ~/famclaw
touch config.yaml        # start empty — the web wizard fills it in
~/.local/bin/famclaw --config config.yaml
```

Open `http://localhost:8080` and complete the setup wizard.

## Install by hand

```sh
# Apple Silicon (Intel Macs: famclaw-darwin-amd64)
cd ~/Downloads
curl -fsSLO https://github.com/famclaw/famclaw/releases/latest/download/famclaw-darwin-arm64.tar.xz
curl -fsSLO https://github.com/famclaw/famclaw/releases/latest/download/checksums.txt
shasum -a 256 -c <(grep 'famclaw-darwin-arm64\.tar\.xz$' checksums.txt)
tar -xJf famclaw-darwin-arm64.tar.xz
xattr -c famclaw                 # drop any quarantine attribute (see below)
codesign -f -s - famclaw         # re-apply a valid local ad-hoc signature
mkdir -p ~/.local/bin
mv famclaw ~/.local/bin/famclaw
~/.local/bin/famclaw --version   # prints a version, exits 0 — not 137
```

Add `~/.local/bin` to your `PATH`, or use the full path.

## What the two security commands do

- `xattr -c famclaw` clears the file's extended attributes — including
  `com.apple.quarantine`. You need it only when the file (or the tarball it
  came from) was fetched by a **browser**; a `curl` download carries no
  quarantine attribute and the command is a harmless no-op.
- `codesign -f -s - famclaw` replaces the shipped ad-hoc signature with a
  fresh local one. Apple Silicon refuses to run any binary whose code does
  not match its signature, and a plain `cp` onto an existing binary (or any
  partial in-place modification) breaks the signature — the process is then
  killed with `Killed: 9` (exit 137). Re-signing after any manual copy or
  rename restores a valid signature. The official updater and installers use
  atomic renames for exactly this reason; prefer them (`scripts/update.sh`).

## Already seeing `Killed: 9`?

If you copied or replaced the binary in place and it now dies instantly:

```sh
codesign -f -s - "$(command -v famclaw || echo ~/.local/bin/famclaw)"
```

If it still dies, probe per the exit-137 section of
`docs/TROUBLESHOOTING.md`.

## Running as a service (optional, developers)

`make install-launchd` installs a launchd agent from a source checkout (see
the Makefile); `scripts/update.sh` finds and restarts it on upgrades.
