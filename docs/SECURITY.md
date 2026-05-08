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
