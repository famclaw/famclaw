---
name: image-understanding
description: Enable image understanding capabilities for describing/analyzing images, reading text, and answering questions about visual content.
version: "0.1"
author: famclaw
tags: [vision, image, multimodal]
platforms: [linux, darwin]
---
# Image Understanding

This skill enables FamClaw to understand and analyze images provided by users. When a user shares an image (or references one), FamClaw can describe what's in it, read text contained within the image, and answer questions about the visual content.

## Gateway Support

Currently, image understanding is implemented for:
- **Telegram** - Full image attachment support with automatic download and encoding
- **WhatsApp** - Not yet implemented
- **Discord** - Not yet implemented  
- **Web Interface** - Not yet implemented

The feature is designed to be extensible to other messaging platforms.

## Gateway Support

Currently, image understanding is implemented for:
- **Telegram** - Full image attachment support with automatic download and encoding
- **WhatsApp** - Not yet implemented
- **Discord** - Not yet implemented  
- **Web Interface** - Not yet implemented

The feature is designed to be extensible to other messaging platforms.

## How It Works

When a user sends an image through any supported gateway (Telegram, WhatsApp, Discord, or web interface), Famclaw processes the image and includes it as part of the message sent to the configured LLM model. The image understanding capabilities depend entirely on the configured model being vision-capable.

## Requirements

For image understanding to work, the configured LLM model must support vision/image input. This includes models like:
- GPT-4o, GPT-4o-mini (OpenAI)
- Claude 3.x series (Anthropic)
- Gemini Pro Vision (Google)
- Llava series or other vision-capable open models
- Any OpenAI-compatible API endpoint serving a vision-capable model

## Usage

Simply send an image along with your question or prompt. For example:
- "What's in this image?" + [attach image]
- "Read the text in this image" + [attach image with text]
- "How many people are in this photo?" + [attach image]
- "Describe this chart or graph" + [attach image]

## Configuration

No special configuration is needed beyond ensuring your LLM endpoint supports vision. The skill works with whatever vision-capable model is configured in your Famcaw setup.

## Notes

- Image understanding requires a vision-capable model. If your configured model doesn't support image input, the LLM will return an error.
- The image is encoded and sent as part of the standard OpenAI-compatible chat completion request.
- All existing text-only flows remain unchanged and fully compatible.