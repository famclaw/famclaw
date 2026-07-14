---
name: reminder
description: Set reminders for yourself or family members. Natural language time parsing supports relative times ("in 2 hours"), absolute times ("tomorrow 9am", "at 14:30"), and shorthand ("30m", "2h").
version: "1.0"
author: famclaw
license: AGPL-3.0-only
tags: [productivity, family, scheduling]
platforms: [linux, darwin]
requires:
  bins: []
---
# Reminder Skill

Schedule fire-once reminders for yourself or other family members. Reminders persist across restarts and are delivered via your connected gateway (Telegram, Discord).

## When to use

Use this skill when the user asks to be reminded of something at a specific time, or asks you to remind someone else (parent-only).

Examples:
- "Remind me in 30 minutes to take a break"
- "Remind me tomorrow at 9am about the dentist appointment"
- "Remind dad in 2 hours to pick up the kids"
- "Set a reminder for 5pm to start dinner"

## How to invoke

Call the builtin `add_reminder` tool with:
- `when` (required): Natural language time expression
- `message` (required): What to be reminded about
- `for_user` (optional, parent only): Another family member to remind

### Time expression formats

| Format | Example | Description |
|--------|---------|-------------|
| Relative minutes | `in 30 minutes`, `in 5 min` | Minutes from now |
| Relative hours | `in 2 hours`, `in 1h` | Hours from now |
| Relative days | `in 3 days`, `in 1 day` | Days from now |
| Shorthand | `30m`, `2h`, `1d` | Compact notation |
| Absolute today | `at 14:30`, `at 2:30 pm` | Today at time (tomorrow if passed) |
| Tomorrow | `tomorrow at 9am`, `tomorrow 09:00` | Tomorrow at time |
| Day of week | `monday at 10:00`, `next friday 5pm` | Next occurrence of day |
| Today | `today at 17:00` | Today at time |

## Output

Returns JSON with:
```json
{
  "reminder_id": 123,
  "due_at": "2026-01-15T14:30:00Z",
  "message": "take out the trash",
  "for_user": "alice",
  "status": "scheduled"
}
```

## Delivery

When the reminder fires, the message is sent as:
```
⏰ Reminder: take out the trash
```

Delivered via the user's connected gateway (Telegram/Discord) to their DM or group chat.

## Notes

- Reminders are fire-once — they don't repeat
- Parents can set reminders for any family member; children can only set for themselves
- Reminders survive service restarts — any overdue reminders fire on startup
- No separate binary needed — pure prompt-injected tool