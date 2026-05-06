# PLAN — Issue #111 Dashboard UI: Link Unknown Accounts

Spec source: `.task.md` + `internal/web/server.go` (real backend endpoints).

## Reconciliation note

The task spec describes endpoints with underscores (`/api/unknown_accounts`) and a
body shape `{user_id}`. The Go backend (PR #113, already merged) actually exposes
the kebab-case routes — `/api/unknown-accounts`, `/api/unknown-accounts/link` —
and expects body `{gateway, external_id, user_name}` plus an `X-Parent-PIN`
header. Since the constraints forbid touching any Go file, the UI MUST wire to
the real server, otherwise the feature is broken end-to-end (and the manual
smoke test called out in the spec would fail).

I keep the externally-visible JS function signature `linkUnknownAccount(accountId, userId)`
verbatim from the spec; internally it reads the row's `data-gateway` /
`data-external-id` attributes to assemble the real POST body, and treats
`userId` as the FamClaw user `name` (which is the unique identifier in
`/api/users`).

## Phase 1 — markup

Single file: `internal/web/static/index.html`.

Insert a new `<div class="d-section">` block on the parent dashboard immediately
after the existing **Conversations** section (`#conversations-list` block) — this
is the closest analogue to an "accounts / users list" that exists today.

Markup uses existing dashboard CSS classes only (`d-section`, `d-title`,
`d-card`, `empty-state`, `btn`, `btn-approve`).

```html
<div class="d-section">
  <div class="d-title">👤 Unknown Accounts</div>
  <div class="d-card">
    <table id="unknown-accounts-table">
      <thead>
        <tr><th>Gateway</th><th>Display name</th><th>First seen at</th><th>Action</th></tr>
      </thead>
      <tbody></tbody>
    </table>
    <div class="empty-state" id="unknown-accounts-empty">No unknown accounts</div>
  </div>
</div>
```

Two narrow CSS rules co-located with existing dashboard rules to make the
table readable inside `d-card` (full width, padded cells, muted header, divider
rows). No new stylesheet; rules live in the existing `<style>` block under the
"Dashboard (parent)" comment.

## Phase 2 — JS

### `loadUnknownAccounts()`
- GET `/api/unknown-accounts` with `X-Parent-PIN` header.
- Build the table body. Each `<tr>` carries `data-account-id`,
  `data-gateway`, `data-external-id` so the link handler can find them.
- The Action `<td>` contains a `<select id="link-target-{accountId}">`
  populated from `state.users` (refreshing it from `/api/users` if empty),
  plus a `<button>` whose click handler calls
  `linkUnknownAccount(account.id, select.value)` via `addEventListener`.
- All text injected via `.textContent` / `option.value` / `option.textContent`.
  Zero `innerHTML`, zero inline `onclick`.
- If list is empty, show the `#unknown-accounts-empty` placeholder.

### `linkUnknownAccount(accountId, userId)`
- Look up the row by `data-account-id`; pull gateway + external_id from
  dataset.
- Resolve a PIN (reuse `requestPIN` modal — same pattern as install/remove
  skill).
- POST `/api/unknown-accounts/link` with
  `{gateway, external_id, user_name: userId}` and the PIN header.
- On 200: remove the row, re-fetch `/api/users`, re-render user grid,
  toast "Linked."
- On non-200: toast the server error.

### SSE hook
Modify `startSSE` (the only place that touches the SSE stream) to also handle a
parsed payload with `type === 'unknown_account_added'`. When seen, call
`loadUnknownAccounts()` (only meaningful while the parent dashboard is open,
guarded the same way as the existing `loadDashboard()` call). The existing
`pending_count` payload behavior is unchanged — protocol untouched.

### Dashboard refresh
`loadDashboard()` gets one extra call: `loadUnknownAccounts()`.

## Phase 3 — CHANGELOG

Add a bullet under `## Unreleased` → `### Added` describing the dashboard UI
landing as the follow-up promised in the existing "Unknown-accounts backend"
entry.

## Phase 4 — gates

1. `CGO_ENABLED=0 go build ./...` — sanity (no Go changed).
2. `CGO_ENABLED=0 go test ./... -count=1`.
3. Diff-scoped `grep -c innerHTML` on the new lines of `index.html` → 0.
4. Diff-scoped `grep -c onclick=` on the new lines of `index.html` → 0.
5. `node -e "$(awk '/<script>/,/<\/script>/' index.html | sed '1d;$d')"` exits 0.

## Out of scope (per .task.md CONSTRAINTS)
- No Go file changes.
- No new dependencies.
- No changes to wizard, chat view, or SSE wire protocol.
