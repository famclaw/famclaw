---
name: todo
description: Manage personal and family todo lists via chat
version: "0.1"
author: famclaw
tags: [productivity, family, todo]
platforms: [linux, darwin]
requires:
  bins: []
trigger:
  mode: "keyword"
  keywords: ["todo", "task", "list", "add", "complete", "done", "remove", "delete"]
---
# Todo Skill

Manage personal and family todo lists via chat. Todos are scoped per user — each family member has their own list.

## When to use

Use this skill when the user wants to:
- Add items to their todo list ("add milk to my list", "remember to buy eggs")
- List their todo items ("what's on my todo list", "show my tasks")
- Mark items as complete ("mark milk done", "complete buy eggs")
- Remove items ("remove milk from my list", "delete buy eggs")

## How to invoke

Use the `builtin__todo` tool with the appropriate action:

| Action | Parameters | Description |
|--------|------------|-------------|
| `add` | `text` (string, required) | Add a new todo item |
| `list` | `filter` (string, optional: "all", "active", "completed") | List todo items (default: "active") |
| `complete` | `id` (integer, required) | Mark a todo item as complete |
| `remove` | `id` (integer, required) | Remove a todo item |

## Examples

- User: "Add milk to my todo list" → `builtin__todo` with `action="add"`, `text="milk"`
- User: "What's on my todo list?" → `builtin__todo` with `action="list"`
- User: "Show all my tasks including completed" → `builtin__todo` with `action="list"`, `filter="all"`
- User: "Mark milk done" → `builtin__todo` with `action="complete"`, `id=1`
- User: "Remove buy eggs from my list" → `builtin__todo` with `action="remove"`, `id=2`

## Notes

- Todos are stored per user — each family member sees only their own list
- Completed items are hidden by default; use `filter="all"` or `filter="completed"` to see them
- Item IDs are shown when listing; use those IDs for complete/remove actions