# Capability View Implementation

This document is the single source of truth for the capability view feature: a web view that shows which FamClaw capabilities (text, vision, voice, search, calendar, delivery) are configured and available versus unavailable. It covers the design, implementation plan, and current status.

## Overview

The capability view feature provides a web-based interface that displays which capabilities are currently configured and available versus those that are not. This allows users to quickly understand what features are functional without having to manually check each component.

## Backend API Endpoint

Create a new endpoint `/api/capabilities` that returns structured data about system capabilities:

```go
// CapabilityStatus represents the status of a single capability
type CapabilityStatus struct {
    Configured bool   `json:"configured"`
    Available  bool   `json:"available"`
    Details    string `json:"details"`
}

// CapabilitiesResponse represents the full capabilities response
type CapabilitiesResponse struct {
    Text     CapabilityStatus `json:"text"`
    Vision   CapabilityStatus `json:"vision"`
    Voice    CapabilityStatus `json:"voice"`
    Search   CapabilityStatus `json:"search"`
    Calendar CapabilityStatus `json:"calendar"`
    Delivery CapabilityStatus `json:"delivery"`
}
```

Example response:

```json
{
  "text": { "configured": true, "available": true, "details": "LLM available via Ollama" },
  "vision": { "configured": false, "available": false, "details": "Vision processing requires specialized hardware" },
  "voice": { "configured": true, "available": true, "details": "Speech-to-text via system tools" },
  "search": { "configured": true, "available": true, "details": "Full-text search enabled" },
  "calendar": { "configured": false, "available": false, "details": "Calendar integration not configured" },
  "delivery": { "configured": true, "available": true, "details": "Telegram gateway available" }
}
```

## Capability Detection Logic

The system detects capabilities across:

- **Text Processing**: LLM availability, text generation tools
- **Vision Processing**: Image recognition, vision tool availability
- **Voice Processing**: Speech-to-text, text-to-speech capabilities
- **Search**: Search indexing, search tool availability
- **Calendar**: Calendar integration, scheduling tools
- **Delivery**: Messaging gateway availability, delivery methods

## Frontend / Web UI

Create a web page that displays the capability view:

- Tabular display of all capabilities
- Color-coded status indicators (green/yellow/red)
- Detailed tooltips with explanations
- Refresh functionality to update status

## Integration Points

- Hardware detection module (`internal/hardware`)
- Web server routing (`internal/web`)
- Configuration management (`internal/config`)
- Tool availability checking (`internal/tool`)

## Implementation Status

### Completed

1. Documentation for the capability view design and plan
2. Documentation for the household backup/restore procedure
3. Documentation for Raspberry Pi / Linux installation

### Pending

1. Implement the backend `/api/capabilities` endpoint
2. Implement the capability detection logic
3. Build the capability view web UI
4. Integrate the capability view into the dashboard
5. Test capability detection on different hardware configurations

## Verification

Acceptance criteria:

- One documented supported config (Raspberry Pi/Linux)
- Capability-availability view in web UI
- Backup/restore procedure with tested round-trip
- Second-adult join flow works without terminal

The documentation aspects of this feature are complete. The backend API, detection logic, and UI still require further development.
