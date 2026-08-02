---
name: trello
description: Trello-backed quick todo list — add, list, and complete cards from chat.
version: "0.2"
author: famclaw
license: AGPL-3.0-only
tags: [productivity, family, todo, trello]
platforms: [linux, darwin]
requires:
  bins: []
trigger:
  mode: "keyword"
  keywords: ["todo", "task", "list", "add", "complete", "done", "remove", "delete"]
---
# Trello Todo Skill

A Trello-backed quick todo list. Each family member has their own list on a
shared board. When you say something that looks like a task, it is captured as
a card on the right list — for the right person — so nothing gets lost.

## Routing: which list gets the card

`trello_add_card` routes by **person**, not by a raw list id. The captain
configured a name→list-id map (in `skills.credentials.TRELLO_LISTS`), so the
model passes a person's name and the skill resolves it to the right list:

- **Explicit person** — if `person` matches a configured name (e.g. `Julia`),
  the card lands in **that person's** list. (You know who you are talking to;
  use that name when the task is for the speaker.)
- **No person / unmapped name** — the card goes to the default list
  (`TRELLO_LIST_ID` / `Backlog`).
- **List names work too** — `person: "Backlog"` is valid and routes to the
  shared Backlog.

> The title should be the task only. The skill strips a trailing "(For Julia)"
> from the title so the target lives in `person`, not in the title. This stops
> the old bug where the target was buried in the title and the card landed in
> the wrong list.

Valid `person`/`list_id` names are listed in `TRELLO_LISTS` (e.g. `Julia`,
`Ilya`, `Teo`, `Backlog`, `Done`).

## Tools

### trello_add_card
Create a card on the right Trello list for the right person.

Use this whenever the user wants to capture a task — for example:
- "Add this to the todo"
- "Remember to buy milk"
- "todo: call the dentist"
- "Add a task for Julia to book the dentist"

Arguments:
- `title` (required) — the task / what to do. Keep the target person OUT of
  the title; use `person` instead.
- `description` (optional) — extra detail for the card.
- `person` (optional) — the family member (or list name) this task is for.
  Set this to the person it's for, or to the name of the family member you're
  talking to when it's their own task. Omit for the default list (Backlog).
- `allow_duplicate` (optional, default false) — if true, creation is forced
  even when an open card with the same title already exists.

**Idempotency:** before creating, the skill checks whether an open card with
the same title already exists on the target list. If it does, the existing
card is returned (with its id) and no duplicate is created. This is what
prevents repeated tool calls from producing duplicate cards. Only `closed`
cards are ignored for the duplicate check.

### trello_list_cards
List the cards currently on a Trello list.

Use this when the user asks:
- "What's on my todo?"
- "Show me my tasks"
- "What do I need to do?"
- "List my todos"
- "What's on Julia's list?"

Arguments:
- `list_id` (optional) — a list name (e.g. `Backlog`, `Julia`, `Done`) or a
  24-char hex list id. Omit to use the default list.

### trello_complete_card
Move a card to the "done" list, marking it complete.

Use this when the user says they finished a task — for example:
- "I completed buying milk"
- "Mark the dentist call as done"
- "X is finished"

Arguments:
- `card_id` (required) — the Trello card ID or short link, as shown by
  trello_list_cards.

## Workflow

1. **Add** — when the user says "add this to the todo" (or similar), take the
   thing they want to remember as the title and call trello_add_card. Set
   `person` to the right family member (or omit for the default Backlog list).
2. **Review** — when asked what's pending, call trello_list_cards and read back
   the cards so the user can pick one to complete.
3. **Complete** — when the user says a task is done, find its card_id from the
   last list and call trello_complete_card to move it to the done list.

## Validation & error handling

- `list_id`/`person` must be a configured name or a 24-char hex id. A value
  that is neither (e.g. a typo) returns a clear error naming the valid lists —
  it is NOT silently passed to the Trello API.
- A 24-char hex id that is not one of the configured lists also errors
  (with the valid lists listed), so a wrong id can't silently target someone
  else's list.
- Every tool returns an explicit, actionable error. Silent empty results (which
  caused the retry spiral) do not happen.

## Configuration

Trello credentials are never stored in the skill file. They are injected by
famclaw from `skills.credentials` (see the config example in this bundle). The
MCP server reads them from the environment:
- TRELLO_API_KEY — your Trello API key
- TRELLO_TOKEN — your Trello OAuth token
- TRELLO_BOARD_ID — the board to operate on
- TRELLO_LIST_ID — the default list for new cards (Backlog)
- TRELLO_DONE_LIST_ID — the list completed cards are moved to
- TRELLO_LISTS — JSON object mapping list/person names to list ids, e.g.
  `{"Backlog":"68e...","Julia":"68e...","Done":"68e..."}`. Drives name
  resolution and per-person routing.

If credentials are missing, the tools return a clear error instead of guessing.
If `TRELLO_LISTS` is missing or malformed, name resolution is disabled and
only raw 24-char hex ids are accepted for `list_id` (the server still starts).

## Parent notes

- Every family member has their own list on the shared board. Route by name
  via `person` — a card for Teo lands in Teo's list, not the shared Backlog.
- Completion moves a card to the configured done list (`TRELLO_DONE_LIST_ID`
  or `TRELLO_LISTS["Done"]`).
- Because the skill deduplicates by title, adding the same task twice is safe:
  the second call returns the existing card instead of creating a duplicate.
