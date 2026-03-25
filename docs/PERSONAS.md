# Family Profiles & Personas

FamClaw gives each family member their own AI experience, age-appropriate and policy-controlled.

## Adding family members

Edit `config.yaml`:

```yaml
users:
  - name: dad
    display_name: "Dad"
    role: parent
    pin: "1234"

  - name: emma
    display_name: "Emma"
    role: child
    age_group: age_8_12
    color: "#f59e0b"

  - name: lucas
    display_name: "Lucas"
    role: child
    age_group: under_8
    color: "#10b981"

  - name: sofia
    display_name: "Sofia"
    role: child
    age_group: age_13_17
    color: "#ec4899"
```

Restart after editing:
```bash
sudo systemctl restart famclaw
```

---

## Roles

| Role | Permissions |
|------|-------------|
| `parent` | All topics allowed. Can approve/deny child requests. Access to parent dashboard. |
| `child` | Filtered by age group. Restricted topics blocked or require approval. |

---

## Age groups

| Age Group | Risk: none | Risk: low | Risk: medium | Risk: high | Risk: critical |
|-----------|-----------|----------|-------------|-----------|---------------|
| `under_8` | Allow | Block | Block | Block | Hard block |
| `age_8_12` | Allow | Allow | Needs approval | Block | Hard block |
| `age_13_17` | Allow | Allow | Allow | Needs approval | Hard block |
| `parent` | Allow | Allow | Allow | Allow | Allow |

**Hard block** = sexual content, self-harm, hate speech, illegal activity. Cannot be overridden, even with approval.

---

## Linking gateway accounts

After adding a user in config, link their messaging accounts:

### Via web dashboard
1. Open `http://famclaw.local:8080`
2. Go to Parent Dashboard → Family
3. Click "Link Account" next to the user
4. Enter the gateway (telegram/discord) and their platform user ID

### Via config
```yaml
# Not yet supported in config — use the web dashboard or API
```

### Finding platform IDs

**Telegram:** Have the user message the bot. Check logs:
```bash
sudo journalctl -u famclaw | grep "unknown account.*telegram"
```

**Discord:** Right-click the user in Discord → Copy User ID (requires Developer Mode in Discord settings).

---

## Per-user model

Each user can have a different LLM model:

```yaml
users:
  - name: lucas
    display_name: "Lucas"
    role: child
    age_group: under_8
    model: "tinyllama"   # lighter model for young kids

  - name: sofia
    display_name: "Sofia"
    role: child
    age_group: age_13_17
    model: "llama3.1:8b"  # more capable for teens
```

If `model` is omitted, the global `llm.model` from config is used.

---

## Parent PIN

Parents authenticate via PIN to access the dashboard:

```yaml
  - name: dad
    role: parent
    pin: "1234"    # change this!
```

The PIN is checked when accessing the parent dashboard in the web UI.

---

## Age-aware system prompts

FamClaw automatically adjusts the AI's personality based on age group:

- **under_8:** Simple vocabulary, encouraging tone, educational focus
- **age_8_12:** Balanced explanation depth, homework help, creative encouragement
- **age_13_17:** More mature topics allowed, deeper explanations, critical thinking encouraged
- **parent:** Full assistant mode, no restrictions
