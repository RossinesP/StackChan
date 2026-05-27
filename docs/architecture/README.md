# StackChan Architecture Documentation

This folder maps the StackChan codebase, with a particular focus on the
LLM/voice pipeline and the **shipped Mistral migration** (PR #1, M1–M10).

| Doc | Scope |
| --- | --- |
| [`01-overview.md`](./01-overview.md) | Top-level system: 4 components, how they relate, where AI lives |
| [`02-firmware.md`](./02-firmware.md) | ESP32-S3 firmware (`firmware/`) — Mooncake apps, HAL, embedded `xiaozhi-esp32` |
| [`03-server.md`](./03-server.md) | Go backend (`server/`) — REST control plane + WS device↔app bridge |
| [`04-app.md`](./04-app.md) | Flutter app (`app/`) — provisioning, agent config, avatar/dance UI |
| [`05-ai-voice-pipeline.md`](./05-ai-voice-pipeline.md) | End-to-end audio + LLM flow across all three components |
| [`06-mistral-migration.md`](./06-mistral-migration.md) | **The headline doc**: full-replace vs partial-replace, side by side. **Path A is the shipped path** |
| [`07-path-a-implementation.md`](./07-path-a-implementation.md) | **Path A build guide + post-mortem**: wire protocol, gateway components, original-plan-vs-shipped milestones |
| [`08-local-dev-setup.md`](./08-local-dev-setup.md) | **Local dev setup**: run the gateway on your laptop, point one StackChan at it, iterate without reflashing. Includes the full env-var cheat sheet for M1–M10 |

Anywhere you see a `[MISTRAL]` callout in the per-component docs, that
component is on the path of any Mistral integration.

## TL;DR

```
Phone App  ──BLE/WiFi/REST──▶ Firmware ══════ direct WSS/MQTT ══════▶ xiaozhi.me cloud
   │                                                                  (LLM + STT + TTS)
   │                                                                       ▲
   ▼                                                                       │
Go Server ──REST──▶ xiaozhi.me   (token broker only — no audio passes here)┘
   ▲
   │  /stackChan/ws  (binary opus + control bridge between Firmware and App)
   ▼
Phone App
```

- The **firmware vendors `xiaozhi-esp32`** as the entire AI runtime
  (`firmware/repos.json`, branch `v2.2.4`). Audio I/O, Opus, wake-word,
  WS/MQTT to LLM, MCP tool routing — all in there.
- The **Go server is a thin shim**: mints xiaozhi developer tokens for the
  firmware and ferries opaque opus/jpeg frames between firmware and phone.
  **Zero AI code, zero audio processing.**
- The **Flutter app is management UI**: BLE provisioning, agent
  configuration, dance choreography, chat history viewer. **No live voice
  loop.**

Therefore replacing xiaozhi with Mistral is **predominantly a firmware
problem**. See [`06-mistral-migration.md`](./06-mistral-migration.md).
