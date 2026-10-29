# Capability View Implementation

This document outlines the implementation of the capability view feature for FamClaw, showing configured vs unavailable capabilities for text, vision, voice, search, calendar, and delivery.

## Overview

The capability view feature provides a web-based interface that displays which capabilities are currently configured and available versus those that are not. This allows users to quickly understand what features are functional without having to manually check each component.

## Implementation Plan

### 1. Backend Integration

Add capability detection endpoints to the web server that can:
- Detect installed tools and libraries
- Identify configured LLM backends
- Check availability of hardware capabilities
- Return structured data about each capability

### 2. Frontend Interface

Create a web-based capability view that:
- Displays capabilities in a tabular format
- Shows current status (configured/available vs not available)
- Provides details about each capability
- Is accessible through the web dashboard

### 3. Capability Detection Logic

The system should detect capabilities across:
- **Text Processing**: LLM availability, text generation tools
- **Vision Processing**: Image recognition, vision tool availability  
- **Voice Processing**: Speech-to-text, text-to-speech capabilities
- **Search**: Search indexing, search tool availability
- **Calendar**: Calendar integration, scheduling tools
- **Delivery**: Messaging gateway availability, delivery methods

## Technical Approach

### Backend Endpoint
Create a new endpoint `/api/capabilities` that returns JSON data like:

```json
{
  "text": {
    "configured": true,
    "available": true,
    "details": "LLM available via Ollama"
  },
  "vision": {
    "configured": false,
    "available": false,
    "details": "Vision processing requires specialized hardware"
  },
  "voice": {
    "configured": true,
    "available": true,
    "details": "Speech-to-text via system tools"
  },
  "search": {
    "configured": true,
    "available": true,
    "details": "Full-text search enabled"
  },
  "calendar": {
    "configured": false,
    "available": false,
    "details": "Calendar integration not configured"
  },
  "delivery": {
    "configured": true,
    "available": true,
    "details": "Telegram gateway available"
  }
}
```

### Frontend Component
Create a React component that:
- Fetches capability data from the backend
- Displays a clean, responsive table
- Uses color coding (green/yellow/red) for status indicators
- Includes tooltips with detailed explanations

## Implementation Steps

1. **Enhance hardware detection**: Extend the existing hardware detection to include capability checks
2. **Add capability endpoints**: Create new API endpoints for capability data
3. **Design frontend UI**: Implement the capability view in the web dashboard
4. **Testing**: Verify capability detection works correctly on different hardware configurations

## Integration Points

- Hardware detection module (`internal/hardware`)
- Web server routing (`internal/web`)
- Configuration management (`internal/config`)
- Tool availability checking (`internal/tool`)