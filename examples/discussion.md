---
title: "The Future of Remote Work"
type: discussion
language: en-US
channel: discussions
total_minutes: 20
segment_max_seconds: 60
# storage picks the research scratchpad backend each discussant uses to stash
# firecrawl findings: "plaintext" (built-in file store, the default) or
# "mongodb" (the MongoDB MCP server declared in mcp.json).
storage: plaintext
host: { name: "Mira", model: "gpt-4o" }
# Each discussant argues the topic from a distinct aspect and answers the
# others by name. They all get firecrawl (web research) + the data-store tool.
discussants:
  - {
      name: "Diego",
      model: "openai/gpt-5.4",
      aspect: "economic / labor markets",
    }
  - {
      name: "Priya",
      model: "openai/gpt-5.4",
      aspect: "team culture & collaboration",
    }
  - {
      name: "Sam",
      model: "openai/gpt-5.4",
      aspect: "urban planning & environment",
    }
  - {
      name: "Lena",
      model: "openai/gpt-5.4",
      aspect: "mental health & work-life balance",
    }
# The commander never speaks — it silently swaps the background image and music
# on the fly to match the mood of the conversation.
commander: { model: "openai/gpt-5.4" }
viewers:
  - { name: "Tom", model: "openai/gpt-5.4" }
---

## Background

Since 2020, remote and hybrid work have reshaped how, where, and when people
work. This panel brings together several perspectives to discuss what the next
decade of remote work looks like — its economics, its effect on teams and
cities, and what it does to the people doing the work. Participants should
research concrete data with their tools and respond to each other directly.
