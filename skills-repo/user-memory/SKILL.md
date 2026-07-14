---
name: user-memory
description: Remember and recall durable facts about the current user across sessions. Stores preferences, ongoing context, and things the user asked you to remember — scoped to each individual user.
version: "1.0"
author: famclaw
license: AGPL-3.0-only
tags: [memory, personalization, per-user]
platforms: [linux, darwin]
trigger_mode: "always"
---
# User Memory

Per-user persistent memory for FamClaw. Each family member gets their own private memory space — what you remember about Teo stays with Teo, what you remember about Maya stays with Maya. Memories survive restarts and are injected into your system prompt so you always have personal context.

## What This Skill Does

- **Remembers** facts the user tells you ("I take my coffee black", "I'm working on Project Phoenix", "My mom's birthday is March 12")
- **Recalls** those facts in future conversations automatically via prompt injection
- **Forgets** specific memories when asked or when they're outdated
- **Scoped per user** — completely isolated from other family members and from shared family state

## When to Use User Memory

### REMEMBER (store a new fact)

Use `remember_user_memory` when the user explicitly asks you to remember something, or when they share a durable preference/fact that will be useful in future conversations.

| Trigger | Example |
|---------|---------|
| "Remember that I..." | "Remember that I prefer dark mode" |
| "Don't forget..." | "Don't forget my cat's name is Whiskers" |
| "My [preference] is..." | "My favorite color is blue" |
| "I'm working on..." | "I'm working on the Q4 budget" |
| Explicit "store this" | "Store this: my API key is sk-..." |

**Categories to use:**
- `preferences` — UI/theme, food, communication style, defaults
- `projects` — Ongoing work, personal projects, goals
- `reminders` — Dates, recurring things, "ask me about X later"
- `context` — Current situation, temporary state ("I'm traveling until Friday")
- `facts` — Personal facts the user volunteers ("I have a peanut allergy")

### RECALL (read memories)

Use `recall_user_memory` when:
- The user asks "What do you remember about me?" or "What's my coffee preference?"
- You need context to answer a question ("Should I use light or dark theme?")
- Starting a new conversation — the system prompt already injects memories, but you can call recall for a specific category
- The user references something vague ("my usual order")

### FORGET (delete a memory)

Use `forget_user_memory` when:
- The user explicitly says "forget that" or "delete that memory"
- A memory is clearly outdated ("I no longer work on Project Phoenix")
- The user corrects a previous memory ("Actually I take cream now")

## Tool Reference

### remember_user_memory(category, label, value)

Stores or updates a memory for the current user.

```json
{
  "category": "preferences",
  "label": "coffee",
  "value": "black, no sugar"
}
```

### recall_user_memory(category?)

Returns all memories for the current user, optionally filtered by category.

```json
{
  "category": "preferences"
}
```

### forget_user_memory(category, label)

Deletes a specific memory.

```json
{
  "category": "preferences",
  "label": "coffee"
}
```

## Memory Injection

Your system prompt automatically includes a `<user_memory>` block with all stored memories for the current user, organized by category. You don't need to call `recall_user_memory` at the start of a conversation — the context is already there.

Example injected block:

```
<user_memory>
- Preferences:
  - coffee: black, no sugar.
  - theme: dark mode.
- Projects:
  - project_phoenix: Q4 budget redesign, due Dec 15.
- Reminders:
  - mom_birthday: March 12 — send flowers.
</user_memory>
```

## Distinction from Family State

| Aspect | User Memory (this skill) | Family State (built-in) |
|--------|-------------------------|------------------------|
| Scope | Per-user private | Shared family-wide |
| Who can read | Only that user | All family members |
| Who can write | That user (or parent) | Parents (children propose) |
| Typical content | Preferences, projects, personal context | Allergies, birthdays, pets, dietary restrictions |
| Approval needed | No | Yes (for children) |

## Guidelines

1. **Don't over-remember** — Only store things that are genuinely durable and useful. Transient chat context doesn't need a memory entry.
2. **Use clear labels** — "coffee" not "my morning beverage preference"
3. **Keep values concise** — The value should be a fact, not a paragraph
4. **Respect privacy** — Never share one user's memories with another user
5. **Correct don't duplicate** — If the user updates a preference, use `remember_user_memory` with the same category+label to overwrite

## Examples

**User:** "Remember I'm allergic to shellfish"
**You:** *calls remember_user_memory(category="facts", label="shellfish_allergy", value="severe — carries EpiPen")*

**User:** "What's my coffee order again?"
**You:** *checks injected <user_memory> block, sees "coffee: black, no sugar" → "You take it black, no sugar."*

**User:** "Forget my coffee preference"
**You:** *calls forget_user_memory(category="preferences", label="coffee") → "ok — forgotten"*