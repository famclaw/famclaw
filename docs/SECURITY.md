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

### Built-in file tool confinement

The built-in file tools (`file_read`, `file_write`, `file_stat`, `file_list`) are confined to the sandbox root via path resolution in the agent layer. Any attempt to access a path outside the sandbox root results in an error.

### Configuration

- `tools.sandbox.root`: Absolute path to the sandbox directory. Defaults to `<database_directory>/skill_sandbox`.
- `tools.sandbox.enabled`: Boolean flag to enable/disable the sandbox launcher for stdio MCP servers. Defaults to `true` (secure-by-default).
- `tools.sandbox.allow_unconfined`: If set to `true`, the sandbox launcher will skip applying restrictions when kernel support is missing (not recommended). Defaults to `false`.

### Environment variable handling

The sandbox launcher constructs a minimal environment for the subprocess using only a hardcoded allowlist of safe variables. The `BuildAllowlist` function (used by both the launcher and the MCP client) ensures that no FamClaw secrets (e.g., `FAMCLAW_LLM_API_KEY`, `*_TOKEN` variables) are passed to the subprocess.
