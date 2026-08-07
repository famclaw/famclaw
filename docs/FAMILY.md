# Guide for Parents

FamClaw is a private AI assistant for your family. This guide explains how it works — no technical knowledge needed.

## How it works

Your family talks to FamClaw through the web browser, Telegram, or Discord. Every message goes through family rules before the AI responds.

- **Young children (under 8):** Only safe topics like homework, stories, and nature
- **Kids (8-12):** More topics allowed, but some need your permission first
- **Teens (13-17):** Most topics allowed, but sensitive ones still need your OK
- **Parents:** Everything is allowed. You manage the family rules.

## What happens when a child asks about something sensitive

1. FamClaw holds the response and sends you a notification
2. You see the request in your dashboard — the child's name, what they asked, and the topic
3. You tap **Approve** or **Deny**
4. If approved, FamClaw answers the child's question
5. If denied, the child sees a friendly "your parent said no" message

## The parent dashboard

Open FamClaw in your browser and select your name. Enter your PIN to access the dashboard.

You'll see:
- **Pending approvals** — requests waiting for your decision
- **Recent conversations** — what each child has been asking
- **Family members** — who's set up and their age group
- **Settings** — change the AI provider, add gateways, manage notifications

## Adding family members

1. Open the dashboard
2. Go to Settings (gear icon)
3. Add a person — enter their name and age group
4. Save

## Linking messaging apps

If your family uses Telegram or Discord, FamClaw can chat there too.

1. Open Settings → Gateways
2. Follow the guide for your messaging app
3. Each family member messages the bot once
4. You link their account to their FamClaw profile

## What FamClaw remembers about your family

FamClaw keeps two kinds of information so it can help without you repeating things every time.

**Family facts** are shared household details — allergies, dietary restrictions, important dates, pets, and any custom categories you add. FamClaw automatically shares safety-critical facts with the AI on every message — by default, this includes **allergies** and **dietary restrictions** — so the assistant always knows, for example, that someone is allergic to nuts before it suggests a recipe. You manage these in the dashboard under Family State. Other facts, like birthdays and pets, stay in FamClaw but are only looked up when a conversation actually needs them.

**User memories** are personal notes your family members ask FamClaw to remember — a nickname, a fear of dogs, a friend's birthday. Each family member's memories are shared with the AI in their own conversations, so FamClaw always remembers what you've told it. You can also ask FamClaw to find a specific memory by keyword — it searches across the label, value, and category of every memory to find what's relevant.

Kids proposing a new family fact go through the usual approval flow; parents can add, update, or delete directly in the dashboard.

## Proactive reminders

FamClaw can remind family members about things at the right time — it doesn't wait for the next message. When a reminder's time comes, FamClaw sends it straight to that family member's messaging app.

Say something natural like:
- "Remind me to take out the trash in 2 hours"
- "Remind me tomorrow at 9am to call Grandma"
- "Remind me at 14:30 to pick up Emma from soccer"

The assistant parses the time (relative like "in 2 hours" or "30m", or absolute like "tomorrow 9am" / "monday 10:00") and the message, then stores the reminder. FamClaw's scheduler delivers due reminders proactively through the recipient's most recently used messaging app.

### Reminding another family member

A parent can set a reminder for someone else in the family:
- "Remind Emma in 10 minutes to come downstairs for dinner"

Only parents can set reminders for other people — children can remind themselves but not others. If the target hasn't messaged FamClaw yet on any gateway, FamClaw tells you it doesn't know how to reach them yet rather than guessing a channel. The feature is powered by the `builtin__add_reminder` tool.

## The assistant can message another family member

On its own initiative — not just in reply to one person — FamClaw can send a message to another family member on their messaging platform. You'll see this, for example, when a reminder fires for someone else, or when the assistant decides a note belongs to a different user.

- **Who can be messaged:** only the family members configured in your Settings → Family. FamClaw will not address anyone outside the family.
- **How it reaches them:** via the recipient's most recently connected app (Telegram or Discord). If a family member hasn't messaged FamClaw yet, FamClaw tells you it doesn't know how to reach them — it never guesses a channel.
- **It's parent-gated:** only a parent's assistant can initiate one of these messages.
- **It's audited.** Every proactive message is recorded in the audit log with who sent it, who it was for, and what it said, and it also appears in the recipient's conversation history in your dashboard.

The feature is powered by the `builtin__send_message` tool.

## Topics that are always blocked

Some topics are never allowed, even with your approval:
- Adult content
- Self-harm
- Hate speech
- Instructions for illegal activities

These are hard-coded into the family rules and cannot be overridden.

## If FamClaw isn't responding

1. Check that your AI provider is running (the dashboard shows connection status)
2. If using a local AI (Ollama), make sure the device it runs on is powered on
3. Restart FamClaw: the web UI has a restart option in Settings

## Privacy

- All conversations stay on your home network
- Nothing is sent to the cloud (unless you chose a cloud AI provider)
- You can see everything your children ask in the dashboard
- No ads, no tracking, no data collection
