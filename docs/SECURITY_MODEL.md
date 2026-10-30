# FamClaw Security Model

## Web session model

FamClaw uses server-side cookie sessions for web UI authentication.

- Cookie name: `famclaw_session`
- Attributes: HttpOnly, SameSite=Strict, Secure (when served over TLS), Path=/
- TTL: 7 days (MaxAge=604800)
- Session IDs: 32 random bytes from crypto/rand, base64url-encoded (43 chars)
- Server-side state: stored in SQLite `web_sessions` table
- Cleanup: expired sessions are deleted hourly by a background goroutine
- No remember-me, no 2FA, no extended sessions in v1

Sessions are invalidated immediately on logout. Expired sessions are cleaned
by the hourly goroutine and also filtered on read (Get returns ErrNoSession
for expired rows).

## Network exposure

### Default bind

`server.host` defaults to `0.0.0.0` (all interfaces). FamClaw's product
shape is a "family box on the LAN" that phones, tablets, and children reach
from other devices, so a fresh install that refused LAN connections would
be a bigger product break than the exposure. The exposure is contained
instead of avoided:

- **Every authenticated surface is session-gated.** No API route returns
  data or takes an action without a valid `famclaw_session` cookie
  (see Web session model). The only public routes are the login/join
  entry points, first-boot setup, health, and the HMAC-signed `/decide`
  approval link.
- **Public entry points are rate-limited with constant-shape responses.**
  `/login`, `/api/join`, and `/api/setup/unlock` share one per-IP budget
  (5 failures / 15 min → 1 min lockout) and return byte-identical errors
  for "no such credential" and "wrong credential".
- **Boot validation + loud log.** A wildcard bind (`0.0.0.0`, `::`, `*`)
  requires `server.secret` to be set to 32+ characters
  (`Config.Validate` fails boot otherwise), because the secret signs the
  `/decide` approval tokens and a LAN-facing install without it is the
  worst state. The boot log says explicitly which mode is active:
  "listening on all interfaces — LAN/remote access enabled" or
  "listening on loopback only".

**Override for single-operator boxes:** `server.host: 127.0.0.1`
(loopback-only, no secret required) or a specific LAN IP for a
particular interface. Any `server.host` that is not a valid IP fails boot
with a message naming the key.

### Child web access: per-child join codes

Children get a web session without sharing the parent PIN. The parent
issues a short, revocable, per-child **access code** from the dashboard
(Web Access Codes section); the child enters (name + code) on the public
`/join` page and receives the same `famclaw_session` cookie as the parent
flow, bound to the child's user ID.

Storage and lifecycle:

- The code is 8 characters from an unambiguous alphabet (no 0/O/1/I),
  generated server-side (never parent-chosen) — 40 bits of entropy.
- Only `SHA-256(code)`, vault-encrypted (same machine-bound AES-256-GCM
  as the parent PIN), is persisted — in a `vault_secrets` row named
  `user_pin:<childName>`. The plaintext code is returned to the parent
  exactly once, on issue; it is not stored in plaintext anywhere (not the
  DB, not the config, not the logs).
- Re-issuing replaces the row: the previous code is dead immediately.
  Revoking deletes the row. A child session cannot obtain admin
  capabilities — the admin routes are role-gated to base role `parent`
  (`protectParent`), and a child chat agent gets the child's toolset,
  never the admin tools.
- The join endpoint draws from the SAME per-IP rate budget as `/login`,
  and all failure states (unknown name, parent name, unissued code, wrong
  code, vault decrypt failure) return the same 401 body.

The public household directory (`GET /api/users/public`) lists
non-parent users' names/display-names/colors only — no roles, no age
groups — because a child must be able to pick their identity without
credentials.

### WebSocket origin policy

`/api/chat` upgrades are gated by `server.ws_origin_policy`:

- `same-origin` (default): a present `Origin` header must equal the
  server's own `scheme://host` (scheme, host, and port all compared); a
  missing `Origin` is allowed — non-browser clients (curl, scripts,
  native apps) cannot send one, and the SameSite=Strict session cookie is
  the effective cross-site gate.
- `strict`: everything above plus a missing `Origin` is rejected — only a
  browser-grade same-origin handshake can open the socket. Use this on
  hosts the operator wants reachable only from the dashboard itself.

Any other value fails boot (`Config.Validate`). Rejected upgrades get the
upgrader's default 403. The SSE dashboard stream (`/api/stream`) sends no
`Access-Control-Allow-Origin` header at all: it is session-gated and the
SameSite=Strict cookie will not attach to a cross-origin request, so a
wildcard would only claim an allowance the gate already denies.

## PIN storage

The parent PIN is never stored in plaintext. The storage flow:

1. User enters PIN (minimum 4 digits)
2. `SHA-256(PIN)` is computed
3. The hash is encrypted with the machine-bound vault key (AES-256-GCM)
4. The encrypted blob is stored in the `vault_secrets` SQLite table

On login, the stored ciphertext is decrypted and `subtle.ConstantTimeCompare`
compares the stored hash with `SHA-256(entered_PIN)`.

The vault key is derived via HKDF-SHA256:
- IKM: machine ID (platform-specific, see Machine-binding section)
- Salt: `famclaw-cred-v1` (literal bytes — changing this invalidates all vaults)
- Info: `vault` (literal bytes — must be lowercase)
- Output: 32 bytes (AES-256 key)

Nonces are 12 fresh random bytes per encryption (from crypto/rand). The output
format is `nonce || ciphertext || GCM_tag` (12 + len(plaintext) + 16 bytes).

## Machine-binding

The vault key is derived from the machine's hardware identifier:

| Platform | Source |
|---|---|
| Linux | `/etc/machine-id` (trimmed) |
| macOS | `IOPlatformUUID` from `ioreg -rd1 -c IOPlatformExpertDevice` |
| Windows | `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid` |

**Why machine-binding?** If the SQLite database file is exfiltrated to another
machine, the vault ciphertext cannot be decrypted — the HKDF key is different.
This limits the blast radius of a database leak.

## Recovering from a machine change

If the machine ID changes (hardware replacement, VM migration, SD card move),
the binary detects `ErrMachineMismatch` at startup and shows the unlock page.

**Recovery procedure:**

1. Boot the binary on the new hardware (or with the moved database).
2. The web UI automatically shows the "Machine Fingerprint Changed" unlock page.
3. Enter the parent PIN. The vault is re-encrypted with the new machine ID.
4. A `[SECURITY]` warning is logged to stderr with timestamp.
5. The web UI redirects to the dashboard normally.

**If you see the unlock page unexpectedly (no hardware change):**

Power off the device immediately. This may indicate the database file was
copied to another machine. Restore the database from a backup and rotate the PIN.

**Losing the PIN after a machine change:**

There is no out-of-band recovery. If the PIN is forgotten after a machine change,
delete the database file (`data/famclaw.db`) and re-run first-boot. All session
history and approvals will be lost.

## Threat model and limitations

**What machine-binding protects against:**
- Database exfiltration: an attacker who copies the `.db` file to another machine
  cannot decrypt credentials without knowing the PIN AND having the original
  machine ID.

**What machine-binding does NOT protect against:**
- Physical access + PIN knowledge: an attacker who has the PIN and physical access
  to the machine can unlock the vault normally.
- Root compromise: an attacker with root access on the original machine can read
  `/etc/machine-id` and derive the vault key.

**Unlock flow trust model:**

When the machine ID changes, the old HKDF key is permanently gone. The stored
ciphertext is undecryptable on the new machine — by design. The unlock flow
therefore CANNOT cryptographically verify the entered PIN against the old ciphertext.

The defence is: rate-limiting (5 attempts / 15 min / IP, 1-min lockout — same
as `/login`) plus a mandatory `[SECURITY]` audit-log line on stderr. An attacker
with physical access and knowledge of the PIN can re-bind the vault — this is the
same trust assumption as the original first-boot. Operators should monitor stderr
for the `[SECURITY]` sentinel.

**Rate limiting:**

Both `/login` and `/api/setup/unlock` share the same in-memory rate limiter
(5 attempts per 15-minute window per IP, then 1-minute lockout). The limiter
resets on process restart — for persistent brute-force protection, place the
device behind a network firewall.

## Credential vault: cryptographic scope (v0.5.x)

The vault is a **UX boundary**, not a confidentiality boundary against a
local attacker. This has been raised on review (see
[CodeRabbit thread on PR #129](https://github.com/famclaw/famclaw/pull/129)
and [issue #130](https://github.com/famclaw/famclaw/issues/130)) and is
accepted as a v0.5.x trade-off.

### What "machine-derived key" means concretely

The vault key derivation lives at `internal/credstore/vault.go:57` in
`deriveKey()`. It calls HKDF-SHA256 with:

- **IKM** = the machine ID string returned by the platform-specific
  `MachineID()` implementation (Linux: `internal/credstore/machineid_linux.go:15`,
  macOS: `internal/credstore/machineid_darwin.go:14`,
  Windows: `internal/credstore/machineid_windows.go:15`,
  other: `internal/credstore/machineid_other.go:8` returns an error).
- **Salt** = literal bytes `famclaw-cred-v1`.
- **Info** = literal bytes `vault`.

HKDF does not add entropy. Anyone who can read the machine ID source
(`/etc/machine-id`, `IOPlatformUUID`, or `HKLM\...\MachineGuid`) and the
SQLite `vault_secrets` table can reconstruct the vault key.

### Why v0.5.x ships it this way

The design goal is a specific UX: **if the binary moves to a new machine,
the parent sees an unlock screen instead of a crash**. Machine-binding
delivers that: the machine ID changes → `Decrypt` returns
`ErrMachineMismatch` → the unlock flow prompts for PIN re-entry.

The threat model deliberately excludes "attacker has local shell on the
same host". At that trust level, the attacker can also read the SQLite
database, the source, and the running binary's memory — the vault would
not be the weakest link.

### v0.6+ considerations (not shipping in v0.5.x)

Real cryptographic secrecy requires binding the key to something an
attacker with local read access cannot see. Options and why each is
deferred:

1. **Machine-derived KEK + persisted random DEK** — file-access still =
   key-access, so this does not actually improve the threat model. Not
   worth the code churn.
2. **Platform keystore** (macOS Keychain via Security framework, Linux
   secret-service via libsecret, Windows DPAPI) — real secrecy against a
   non-root local attacker on macOS and Windows; on Linux it depends on
   the desktop keyring being unlocked. **Requires CGO**, which conflicts
   with the project's `CGO_ENABLED=0` cross-compile rule. Adopting it
   means splitting the release into a CGO-enabled desktop build and a
   pure-Go headless/server build — a larger architectural decision, not
   just a swap.
3. **TPM / Secure Enclave** — hardware-bound secrecy plus availability
   constraints (not every FamClaw target has a TPM). CGO + platform
   detection + fallback logic. Almost certainly out of scope for v0.6.

Until the CGO trade-off decision is made, the vault stays as it is and
this section stays honest about what it does and does not defend against.

## Sandbox / process confinement

FamClaw uses a landlock+seccomp-based sandbox to confine MCP stdio-server subprocesses and restrict built-in file tool access to a designated directory.

### MCP server sandboxing

When `tools.sandbox.enabled` is true (the secure-by-default setting), FamClaw launches stdio-based MCP skill servers through a sandbox launcher that applies:

- **Landlock filesystem rules**: The subprocess gains read/write access only to the configured sandbox root (default: a subdirectory of the database directory) and execute access to standard system paths (`/bin`, `/usr/bin`). All other filesystem access is denied.
- **Seccomp network filter**: The subprocess is forbidden from performing network-related syscalls (socket, bind, connect, etc.), preventing any outbound network connections.

The sandbox launcher is the FamClaw binary itself, re-executed with the `-sandbox-launcher` flag. It receives a minimal allowlisted environment (only `HOME`, `LANG`, `PATH`, `TERM`, `TMPDIR` are forwarded; all secrets and FamClaw-specific environment variables are stripped).

If the host kernel lacks landlock or seccomp support, FamClaw refuses to start rather than run an unsandboxed subprocess (fail-closed behavior). Operators who genuinely want unconfined subprocesses must explicitly set `tools.sandbox.enabled: false`.

### MCP internal policy as compensating boundary

On hosts where kernel sandboxing is unavailable — macOS has no landlock or seccomp; on Linux a sandboxed adb server cannot bind its local sockets — operators set `tools.sandbox.allow_unconfined: true` so that stdio MCP servers launch without the OS sandbox. This is an explicit, logged opt-out, and it removes the kernel-level filesystem and network isolation around the MCP subprocess.

For `android-control` (the family phone driver) this is compensated by the MCP server's **own policy engine**, which is the containment boundary in unconfined mode:

- **Default-deny app allowlist** with per-app modes (`full`, `read_only`, `deny`); a named `deny` entry always wins. Actions only execute against the current foreground package and only when that package is allowed for that mode.
- **Per-tool kill-switches** (`tools:` map in the policy file); `shell` (arbitrary `adb shell`) is off by default and never exposed to the LLM.
- **Device-state guardrails**: mutating tools refuse unless the screen is on and unlocked (`refuse_when_off` is the v1 default).
- **Rate limiting** (`max_actions_per_turn`, `min_interval_ms`) bounds how fast the device can be driven.
- **No credential-shaped input**: `type` payloads matching OTP/PIN/password patterns are rejected with a fixed message.
- **No-leak scrubbing**: the device serial is replaced with a placeholder in all tool results and errors; the device is addressed by a stable alias, never a serial.

Policy-level containment constrains *which adb commands the server will execute*; it is not kernel isolation, so an unconfined host grants the subprocess its full host-level filesystem and network access. Run `allow_unconfined` only on a host whose compromise is acceptable to the family (the v1 target host is the operator's own machine with an authorized adb connection to the family phone).

### Built-in file tool confinement

The built-in file tools (`file_read`, `file_write`, `file_stat`, `file_list`) are confined to the sandbox root via path resolution in the agent layer. Any attempt to access a path outside the sandbox root results in an error.

**Per-conversation isolation.** Each DM and each channel/group is confined to its own subdirectory under the sandbox root (`conversations/<gateway>/<identity>`), so one conversation cannot read another conversation's files. The conversation identity (gateway, external ID, group ID) is allowlisted via a strict `[A-Za-z0-9._-]` character set and containment is asserted before any file operation. Family facts and user memories remain global — partitioning only affects the `file_*` tools.

> **Containment is Go-level only.** Landlock applies solely to MCP stdio-server subprocesses launched through the `-sandbox-launcher`; the `file_*` handlers run in the main process and are guarded only by `confinePath` path validation. A crafted gateway ID or group ID cannot traverse out of its subtree because the identity is sanitized before any path is constructed.

### Configuration

- `tools.sandbox.root`: Absolute path to the sandbox directory. Defaults to `./data/sandbox`.
- `tools.sandbox_scope`: How file-tool access is partitioned. `conversation` (default) gives each DM and each channel/group its own subdirectory (`conversations/<gateway>/<identity>`); `global` uses a single shared directory. Legacy per-user/per-group subdirectories are migrated to `conversations/_legacy/` on first boot.
- `tools.sandbox.enabled`: Boolean flag to enable/disable the sandbox launcher for stdio MCP servers. Defaults to `true` (secure-by-default).
- `tools.sandbox.allow_unconfined`: If set to `true`, the sandbox launcher will skip applying restrictions when kernel support is missing (not recommended). Defaults to `false`.

### Environment variable handling

The sandbox launcher constructs a minimal environment for the subprocess using only a hardcoded allowlist of safe variables. The `BuildAllowlist` function (used by both the launcher and the MCP client) ensures that no FamClaw secrets (e.g., `FAMCLAW_LLM_API_KEY`, `*_TOKEN` variables) are passed to the subprocess.

## Web Fetch and Search Security Model

### Host Decision Process

When a user invokes a web fetch or search tool, the system performs a host decision process before allowing the request to proceed. This process determines whether a particular host is allowed to be accessed based on the configured URL policy settings.

#### Allowlist Mode (`url_policy: allowlist`)

In allowlist mode, only hosts explicitly listed in the `url_allowlist` are permitted. This mode provides strict control over which domains can be accessed. The host matching follows these rules:
- Exact host matches are permitted (e.g., `example.com` allows `example.com`)
- Subdomain matches are permitted (e.g., `example.com` allows `www.example.com`)
- The allowlist is evaluated against the final redirected host (after any HTTP redirects)
- An empty allowlist denies all fetches (SSRF guard)

#### Denylist Mode (`url_policy: denylist`)

In denylist mode, all public hosts are allowed by default, with explicit blocks in the `url_denylist`. This provides a secure-by-default experience that can be customized by parents. The denylist includes:
- A bundled kid-safe baseline (Aegis Blocklist) that blocks adult content, hate groups, and harmful sites
- Parent-maintained denylist entries in `url_denylist` (exact or subdomain matches)
- Hosts are matched exactly or as subdomains
- Suspicious site detector (W4) that can block sites based on reputation

The denylist mode is the default when `url_allowlist` is empty, providing a secure-by-default experience.

#### Security Gate Implementation

The host decision process is implemented in the `hostDecision` function in `internal/agent/agent.go`. This function:
1. Evaluates the URL policy configuration (`url_policy` field)
2. Checks if the host is in the allowlist (when `url_policy` is "allowlist")
3. Checks if the host is in the denylist (when `url_policy` is "denylist")
4. Applies the appropriate security decision based on the policy mode and configuration
5. Returns a boolean indicating whether the host is allowed to be accessed

#### Admin Tools

Parent admins can manage web fetch settings via:
- `/api/tools/web_fetch/allowlist` - View/edit allowlist entries
- `/api/tools/web_fetch/denylist` - View/edit denylist entries

These endpoints are only available to users with the "parent" role.

### Web Search Security Model

The web search tool is subject to the same URL policy rules as web fetch. When enabled, it queries a search provider (SearXNG or Tavily) and returns structured results. The security model ensures that:
- Search endpoints are validated against the configured URL allowlist
- The search provider's host is subject to the same security gates as web fetch
- Results are processed through the same security pipeline as web fetch

### SSRF Protection

The system implements robust SSRF protection through:
1. **URL Policy Enforcement**: The `url_policy` configuration controls whether hosts are allowed
2. **Host Validation**: All hosts are validated against the allowlist or denylist
3. **Redirect Handling**: The host decision process is applied to every redirect target
4. **Private Network Blocking**: Optionally block private network access (loopback/RFC1918/ULA) via `tools.web_fetch.block_private_networks`

### Secure-by-Default Configuration

By default, the system operates in denylist mode with a bundled kid-safe baseline. This provides:
- Protection against adult content, hate groups, and harmful sites
- Parent-controlled customization of the denylist
- Minimal risk of accidental exposure to inappropriate content
- Clear security boundaries for web access

This security model ensures that all web tool usage is subject to the configured security policies, with clear separation between different permission levels and security configurations.
