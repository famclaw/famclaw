---
name: family-knowledge
description: Family knowledge base for recording and retrieving household facts (members, allergies, dietary rules, doctors, schedules, house rules, pets, important dates).
version: "0.1"
author: famclaw
license: AGPL-3.0-only
tags: [family, knowledge, memory, household]
platforms: [linux, darwin]
requires:
  bins: []
trigger:
  mode: "always"
---
# Family Knowledge

You have access to a persistent family knowledge base that stores durable household facts. This knowledge is shared across all family members and conversations.

## Built-in Categories (always available)

| Category | Description | Auto-injected |
|----------|-------------|---------------|
| `allergies` | Per-person allergies and severity. Safety-critical. | ✅ Yes |
| `dietary_restrictions` | Per-person or family dietary patterns (vegetarian, kosher, halal, gluten-free, etc.). Safety-critical. | ✅ Yes |
| `important_dates` | Birthdays, anniversaries, recurring family events. | ❌ On demand |
| `pets` | Family pets — names, species, notes. | ❌ On demand |

Parents can also create custom categories via the `add_family_category` tool.

## Available Tools

### Read (all roles)
- **`get_family_state(category?)`** — Read facts. Optional `category` filters to one category; omit to see everything. Returns fact IDs for mutation.

### Propose (all roles)
- **`propose_family_fact(category, subject, label, value, reason)`** — Propose a new fact.
  - **Parent**: Applied immediately (OPA-gated).
  - **Child**: Queued for parent approval; applied after approval.

### Parent-only mutations (admin tools)
- **`set_family_fact(category, subject, label, value)`** — Create or update a fact directly (upserts on `category+subject+label`).
- **`delete_family_fact(id)`** — Delete a fact by its numeric ID.
- **`add_family_category(name, description, always_inject?)`** — Add a custom category. `always_inject=true` makes it auto-injected like allergies.
- **`delete_family_category(name)`** — Delete a custom category (must be empty; built-ins cannot be deleted).

## When to Use Family Knowledge

**USE `get_family_state` when the user asks about:**
- Family member details (names, ages, birthdays)
- Allergies or dietary restrictions
- Pet names, types, care notes
- Important dates (birthdays, anniversaries, appointments)
- House rules or routines
- Doctors, medications, medical info
- Anything the family has previously told you to remember

**USE `propose_family_fact` / `set_family_fact` when the user:**
- Explicitly asks you to remember something ("Remember that...")
- Shares a new fact that should persist ("Mom is allergic to penicillin")
- Corrects an existing fact ("Actually, Teo's birthday is March 12, not March 14")

**DO NOT use family knowledge for:**
- General knowledge questions (weather, facts, news) — use `web_fetch` / `web_search`
- Ephemeral conversation context — the conversation history already handles that
- One-off facts the user doesn't want saved

## Subject Values

The `subject` field must be one of:
- A configured family member's `name` from config (e.g., `teo`, `julia`, `family`)
- The literal string `"family"` for household-wide facts

## Examples

**User:** "What's Teo allergic to?"
→ `get_family_state(category="allergies")`

**User:** "Remember that we have a dog named Biscuit, a golden retriever."
→ `propose_family_fact(category="pets", subject="family", label="Biscuit", value="golden retriever, 3 years old", reason="user told me to remember")`

**User (parent):** "Add a house rule: no phones at dinner."
→ `set_family_fact(category="house_rules", subject="family", label="phones_at_dinner", value="No phones at the dinner table")`

**User:** "When is Grandma's birthday?"
→ `get_family_state(category="important_dates")`

## Workflow for Children

Children **cannot** directly write facts. When a child shares something to remember:
1. Call `propose_family_fact(...)` with a clear `reason`.
2. Tell the child: "I've sent that to your parents for approval. They'll review it and it'll be saved once approved."

## Workflow for Parents

Parents can:
- Use `propose_family_fact` (auto-applies after OPA check)
- Use `set_family_fact` for direct writes
- Use `add_family_category` to create new categories like `house_rules`, `medications`, `school_contacts`, etc.

## Safety Notes

- `allergies` and `dietary_restrictions` are **always injected** into every system prompt via the `<family_safety>` block. The model sees them automatically — you do not need to call `get_family_state` for these unless the user asks for detail.
- Other categories are **on-demand** — call `get_family_state` when relevant.
- Parents can mark custom categories as `always_inject=true` for safety-critical info (e.g., `medications`).

## Category Management

To list existing categories: `get_family_state()` (no filter) shows all categories with their facts.
To add a category (parent only): `add_family_category(name="medications", description="Prescription medications and dosages", always_inject=true)`