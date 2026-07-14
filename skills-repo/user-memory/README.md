# User Memory Skill

Per-user persistent memory for FamClaw. Each family member gets their own private memory space.

## Installation

```bash
famclaw skill install ./skills-repo/user-memory
```

## Tools Provided

- `builtin__remember_user_memory` — Store a memory for the current user
- `builtin__recall_user_memory` — Recall memories for the current user
- `builtin__forget_user_memory` — Delete a specific memory

## Memory Categories

- `preferences` — UI/theme, food, communication style, defaults
- `projects` — Ongoing work, personal projects, goals
- `reminders` — Dates, recurring things, "ask me about X later"
- `context` — Current situation, temporary state
- `facts` — Personal facts the user volunteers

## Distinction from Family State

| Aspect | User Memory | Family State |
|--------|-------------|--------------|
| Scope | Per-user private | Shared family-wide |
| Who can read | Only that user | All family members |
| Who can write | That user (or parent) | Parents (children propose) |
| Typical content | Preferences, projects, personal context | Allergies, birthdays, pets, dietary restrictions |
| Approval needed | No | Yes (for children) |

See [SKILL.md](SKILL.md) for full documentation.