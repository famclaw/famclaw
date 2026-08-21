# Troubleshooting

## FamClaw won't start

**What happened:** The binary exits immediately or the service fails.

**What to check:**
1. Is the config file found? FamClaw loads exactly the file given by `--config` (default `config.yaml` in the current directory). The `~/.famclaw/` and `/opt/famclaw/` locations are used by the systemd/launchd service installed via `make install-service`, which passes `--config` pointing there.
2. Is the server secret set? If empty, FamClaw generates one automatically
3. Check logs: `journalctl -u famclaw -f` (Linux) or `tail -f ~/logs/famclaw.log` (Mac)

**Common causes:**
- Port 8080 already in use → change `server.port` in config
- Database directory doesn't exist → FamClaw creates it automatically, but check permissions

---

## Binary exits with 137 and no output after an upgrade

**What happened:** Upgrading FamClaw overwrote the installed binary while the old
process was still running. The new binary looks valid but exits immediately with
**137** (SIGKILL) and produces no output.

**What to check:**
1. Did the upgrade write the binary directly onto the live path (`cp`/overwrite)
   instead of replacing it via `mv`/rename?
2. Distinguish this from a genuine signing failure with one probe:
   ```sh
   cp <installed-binary> /tmp/famclaw-probe && /tmp/famclaw-probe --version
   ```
   - Works from the clean path → the **install method** is the problem.
   - Still dies → genuine binary/signing problem (see #313). For release
     binaries, `codesign -dv famclaw` should show either a
     `Developer ID Application` authority (signed releases — additionally,
     `xcrun stapler verify famclaw` confirms the stapled notarization
     ticket) or `Authority=adhoc` (ad-hoc fallback releases —
     `docs/RELEASE.md`); a binary with *no* signature at all is not a
     supported release artifact and is a genuine exit-137 cause.

**Why it happens:** `cp` writes *through* the existing inode. The running process
keeps its pages, but the path's content no longer matches what was validated, so
the kernel refuses to `exec` it (exit 137). The condition clears the moment the
old process exits — which is why restarting the service "fixes" it and makes the
symptom look intermittent.

**What to do:** Replace, never overwrite. Download to a temporary file **in the
same directory** as the destination, then `mv` it into place. A same-directory
`mv` is a rename: it is atomic, leaves the running process on its old inode, and
gives the path a fresh one.

```sh
curl -fsSL ... -o /usr/local/bin/.famclaw.tmp.$$
chmod +x /usr/local/bin/.famclaw.tmp.$$
mv -f /usr/local/bin/.famclaw.tmp.$$ /usr/local/bin/famclaw
```

**Time to fix:** About 1 minute — rerun the install/upgrade script (which now
uses an atomic rename), or move the binary manually as above.

---

## Can't reach famclaw.local

**What happened:** Browser shows "site can't be reached."

**What to do:**
1. Wait 1-2 minutes after boot — mDNS needs time to advertise
2. Try the IP address directly: find it with `hostname -I` on the device
3. On Windows: mDNS may need Bonjour installed
4. On Android: mDNS is unreliable — use the IP address

**Time to fix:** About 1 minute.

---

## AI is not responding

**What happened:** You send a message but get no response, or see an error.

**What to check:**
1. Is your AI provider running? Dashboard shows connection status
2. If using Ollama on LAN: is the device powered on? Is Ollama running?
3. If using a cloud provider: is your API key valid? Is the service up?

**What to do:**
- Open Settings in the dashboard
- Click "Test connection" next to your AI provider
- If it fails: check the URL and API key

**Time to fix:** About 2 minutes.

---

## Tool calls fail with HTTP 400

**What happened:** The LLM tries to call a tool (`web_search`, `web_fetch`, etc.) but the request is rejected, often with an HTTP 400 error.

**What to check:**
1. Strict backends (vLLM, OpenAI, Azure) reject tool calls that don't follow the OpenAI spec exactly. FamClaw now serializes tool-call arguments as a JSON-encoded **string** (not a bare object) and sends tool names without the internal `builtin__` prefix, so tool calls work on these backends as of v0.8.0.
2. If you still see HTTP 400 on tool calls, you are running an older release — update FamClaw.

**Time to fix:** Update FamClaw (`curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/update.sh | bash`).

---

## Response ends with "no action was completed"

**What happened:** The assistant tried one or more tools, but every tool call failed, so the response ends with:

> (Note: no action was completed - every tool call in this turn failed.)

**What to know:**
- This note is appended whenever all tool calls in a turn fail and the model claimed it succeeded — it triggers on the tool loop's exit reason, not on any keyword, so it behaves the same in every language.
- Common causes: a tool blocked by policy, an unavailable endpoint or host, or a tool that returned an error.
- Check the tool's requirements — for example `web_fetch` needs a non-empty `url_allowlist`, and `web_search` requires `tools.web_fetch.enabled=true`.

**Time to fix:** About 1 minute (fix the tool config or unblock the host).

---

## Child sees "waiting for parent" but parent didn't get a notification

**What happened:** The approval notification didn't reach you.

**What to check:**
1. Are notifications configured? Open Settings → Notifications. If FamClaw
   logged a `[notify] WARNING` at startup, no channel is enabled — add one
   under `notifications:` in `config.yaml` (email, slack, discord, sms, ntfy).
2. If using Telegram: is the bot still connected?
3. Check the dashboard directly — pending approvals are always shown there

**Quick fix:** Open the dashboard and approve/deny directly. No notification needed.

---

## Messages are slow

**What happened:** Responses take more than 10 seconds.

**Possible causes:**
- Local AI model too large for your hardware → try a smaller model in Settings
- Network issues to cloud provider → check your internet connection
- Multiple family members chatting simultaneously → this is normal, each gets their own queue

**Recommended models by hardware:**
- RPi 5 8GB: `qwen3:1.7b`
- RPi 4 4GB: `qwen3:1.7b`
- 16 GB Mac: `qwen3:4b`
- 64 GB Mac: `gemma4:31b`

---

## How to update FamClaw

Run:
```bash
curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/update.sh | bash
```

This downloads the latest version, verifies it, and installs it. If the new version fails to start, it automatically rolls back to the previous version.

---

## How to reset everything

If you want to start fresh:
1. Stop FamClaw: `sudo systemctl stop famclaw`
2. Delete the database: `rm ~/.famclaw/data/famclaw.db`
3. Delete the config: `rm ~/.famclaw/config.yaml`
4. Start FamClaw: `sudo systemctl start famclaw`
5. The setup wizard will appear again
