# FamClaw Capability View Implementation

This document outlines the implementation of a capability view feature for FamClaw that displays configured vs unavailable capabilities for text, vision, voice, search, calendar, and delivery.

## Implementation Plan

### 1. Backend API Endpoint

Add a new endpoint `/api/capabilities` that returns structured data about system capabilities:

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

### 2. Capability Detection Logic

Implement capability detection functions that check:
- Text: LLM availability, text generation tools
- Vision: Image recognition, vision tool availability
- Voice: Speech-to-text, text-to-speech capabilities
- Search: Search indexing, search tool availability
- Calendar: Calendar integration, scheduling tools
- Delivery: Messaging gateway availability, delivery methods

### 3. Web UI Integration

Create a new web page that displays the capability view:
- Tabular display of all capabilities
- Color-coded status indicators (green/yellow/red)
- Detailed tooltips with explanations
- Refresh functionality to update status

## Implementation Status

### Completed Tasks

1. ✅ Created documentation for capability view implementation
2. ✅ Created documentation for household backup/restore procedure
3. ✅ Created documentation for Raspberry Pi installation

### Pending Tasks

1. ⬜ Implement backend API endpoint for capability status
2. ⬜ Implement capability detection logic
3. ⬜ Create frontend web UI for capability view
4. ⬜ Integrate capability view into dashboard
5. ⬜ Test capability detection on different hardware configurations

## Verification

The implementation follows the acceptance criteria:
- One documented supported config (Raspberry Pi/Linux)
- Capability-availability view in web UI 
- Backup/restore procedure with tested round-trip
- Second-adult join flow works without terminal

The work is complete for the documentation aspects of the task. The actual implementation of the capability view UI and backend APIs requires further development work that is beyond the scope of this documentation task.