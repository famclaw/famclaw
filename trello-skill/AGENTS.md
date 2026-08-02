# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

- Add durable project-specific notes here as they are discovered through real work.

## Project notes

- **Separate Go module** (`github.com/famclaw/trello-skill`). From the worktree root: `cd trello-skill && CGO_ENABLED=0 go build ./...` and `go test ./...`. It is NOT compiled into the famclaw binary; famclaw loads it at runtime via SKILL.md + the stdio MCP server.
- **Validation before the API.** `Resolver` (internal/trello/resolver.go) resolves list_id/person to a Trello list id and validates the value before any HTTP call. A non-hex, non-name value errors and makes no API call; a 24-char hex id not in the configured TRELLO_LISTS map also errors. The HTTP Client stays a thin transport.
- **Idempotency.** trello_add_card lists the target list first and returns the existing open card (never duplicates). Card.Closed is parsed from Trello and excluded from the dedup match. allow_duplicate=true forces a create.
- **Per-person routing.** Driven by TRELLO_LISTS (name-to-id JSON in env). Priority: explicit person argument > default (TRELLO_LIST_ID/Backlog). An unmapped person falls back to the default list with a reported note. The model supplies the speaker's name (it knows who it is talking to via famclaw's prompt).
- **Title hygiene.** cleanTitle strips a trailing "(For name)" when name is a known list/person, so the target lives in the person argument, not the title.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
