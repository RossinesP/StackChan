# 06 — Mistral Migration: Two Paths

This doc compares **two realistic strategies** for replacing xiaozhi.me's
LLM + STT + TTS with Mistral's APIs. Both end at the same outcome (a
working voice assistant on the StackChan robot), but they differ
dramatically in effort, blast radius, and which parts of the existing
ecosystem you keep.

> **Premise**: xiaozhi.me provides LLM + STT + TTS over a custom realtime
> protocol (WS or MQTT). Mistral provides chat completions, audio STT,
> and audio TTS over standard HTTP/WebSocket APIs. We need a bridge.

## The two paths at a glance

| | **Path A — Partial swap** (gateway) | **Path B — Full firmware replacement** |
| --- | --- | --- |
| **Where the change happens** | Mostly **server-side** + small firmware redirect | Almost entirely **firmware-side** |
| **Firmware changes** | Redirect `OTA_URL`, optional protocol patch | Replace `start_xiaozhi_app()`; potentially rip out `xiaozhi-esp32` entirely |
| **Server changes** | Add Mistral gateway: realtime WS endpoint translating xiaozhi protocol ↔ Mistral APIs | Optional |
| **App changes** | Minimal (rebase `XiaoZhi_util.dart` URLs if you also re-host config) | Same as A |
| **Keeps xiaozhi-esp32 vendored** | Yes (intact) | No (or only the audio I/O parts) |
| **OTA, wake-word, audio I/O** | Untouched | You own them |
| **Effort estimate** | Medium (1 dev × few weeks for the gateway) | High (1 dev × months; embedded C++ + protocol design) |
| **Risk** | Low — firmware is unchanged. Worst case: revert `OTA_URL`. | High — bricked devices, audio glitches, MCP bugs |
| **Maintainability** | Bound to xiaozhi protocol changes (vendor at v2.2.4) | You own the stack; no upstream churn |
| **Reuses Mistral standard APIs** | Yes (server uses `chat/completions`, audio APIs) | Yes (firmware does, more directly) |

## Path A — Partial swap via a translation gateway

### Architecture

```mermaid
flowchart LR
    FW["Firmware<br/>(unchanged xiaozhi-esp32)"]
    GW["Mistral Gateway<br/>(new — Go or any lang)<br/>Speaks xiaozhi protocol on one side,<br/>Mistral APIs on the other"]
    M["Mistral APIs<br/>(chat + audio)"]

    FW -- "1\. GET /xiaozhi/ota/<br/>(returns WS URL)" --> GW
    FW <-. "2\. WSS hello/listen,<br/>opus frames in/out,<br/>MCP tool calls" .-> GW
    GW -- "3\. Opus → PCM → STT (audio API)" --> M
    M -- "4\. Transcript" --> GW
    GW -- "5\. chat/completions<br/>(with MCP tools as functions)" --> M
    M -- "6\. Reply text + tool_calls" --> GW
    GW -- "7\. text → TTS (audio API) → PCM → Opus" --> M
    GW -. "8\. Opus + JSON state back" .-> FW
```

### Implementation steps

1. **Build a Mistral gateway** that speaks the xiaozhi protocol. The
   wire-level reference lives in
   `firmware/xiaozhi-esp32/main/protocols/{websocket_protocol.cc,
   mqtt_protocol.cc, protocol.cc}` — start by replicating its message
   types: `hello`, `listen` start/stop, binary opus frames, `tts` state
   events, `mcp` JSON-RPC.
2. **Decode opus to PCM**, stream to Mistral STT.
3. **Run Mistral chat completions** with the agent's system prompt
   (loaded from your local DB, populated by the Flutter app via the
   existing CRUD endpoints).
4. **Translate MCP tool definitions to Mistral function-calling
   schema**. When the model returns `tool_calls`, forward as MCP
   JSON-RPC over the WS to the firmware; when the firmware replies with
   `tool_result`, feed back into the next chat turn.
5. **Stream Mistral TTS**, encode PCM → Opus (libopus), frame and
   send.
6. **Redirect the firmware to your gateway**: change the build's
   `CONFIG_OTA_URL` (`firmware/main/Kconfig.projbuild:5`) to your
   gateway's OTA endpoint. The OTA response payload (per
   `xiaozhi-esp32/main/ota.cc`) is what tells the firmware which
   WS/MQTT URL to dial. Your gateway returns its own URL.

### Pros

- **The firmware is essentially unchanged** — you can ship A/B builds
  by flipping a Kconfig.
- The Go server's existing `/stackChan/ws` and REST plane keep working
  for the App.
- You can co-locate the gateway with the existing Go server (single
  process) or run it standalone.
- Easier to debug: gateway is plain Go/Python you control, with logs
  on your terms.

### Cons

- You inherit the xiaozhi wire protocol (which is undocumented and
  pinned to v2.2.4 — see `firmware/dependencies.lock`). Any future
  xiaozhi-esp32 version bumps may shift the wire format.
- You pay an extra hop (Mistral chat is request/response, not
  realtime) — interactive latency depends on Mistral's audio APIs
  being streaming-capable. For lowest latency consider Mistral's
  streaming endpoints throughout.
- Server-side opus codec needs CGo + libopus (`go.mod` has none of
  this today).

### Concrete files to add / touch

| Where | Change |
| --- | --- |
| `firmware/main/Kconfig.projbuild:5` | `default "https://api.tenclass.net/xiaozhi/ota/"` → your gateway |
| `server/internal/web_socket/web_socket.go` | Add a third `deviceType=AI` branch (or new handler) implementing the xiaozhi WS protocol |
| `server/internal/xiaozhi/xiaozhi.go:37` | Optional: stub `baseUrl` so REST control plane targets your DB instead of xiaozhi.me |
| `server/go.mod` | Add `github.com/hraban/opus`, Mistral HTTP client, MCP JSON-RPC helper |
| New: `server/internal/mistral_gateway/{ws.go, ota.go, stt.go, tts.go, llm.go, mcp.go}` | The actual gateway logic |

## Path B — Full firmware replacement

### Architecture

```mermaid
flowchart LR
    FW["Firmware<br/>(custom AI runtime,<br/>NO xiaozhi-esp32 — or only its codec layer)"]
    M["Mistral APIs<br/>(chat + audio)"]

    FW <-- "Direct WSS / HTTPS<br/>opus or PCM,<br/>chat/completions,<br/>tool calls" --> M
```

### Implementation steps

1. **Decide what to keep from `xiaozhi-esp32`.** The high-value bits
   are audio I/O codecs, AFE/wake-word, and Opus encode/decode — all
   genuinely hard on ESP32. The replaceable bit is the
   `Application` + `Protocol` layer.
2. **Replace `start_xiaozhi_app()`** in
   `firmware/main/hal/board/hal_bridge.cc:99-109`. New body:
   - Initialize whichever audio codec / processor / wake-word stack
     you keep (you can still call into xiaozhi's classes directly
     since they're compiled in via `firmware/main/CMakeLists.txt`).
   - Open a WSS / HTTP/2 client to Mistral.
   - Run your own state machine (idle → listening → thinking →
     speaking).
3. **Reimplement the avatar/display callbacks**. xiaozhi's
   `Application` is what calls `Display::SetEmotion` and
   `Display::SetChatMessage`, which `stackchan_display.cc` overrides
   to drive the cute avatar. You must call these directly from your
   new state machine to keep the avatar reactive.
4. **Reimplement MCP tool dispatch**, or replace MCP entirely with
   Mistral function-calling on-device. `firmware/main/hal/hal_mcp.cpp`
   currently registers tools into xiaozhi's `McpServer` — re-route to
   your own function dispatcher.
5. **Build a tiny config plane**. The Go server today hands out
   xiaozhi tokens; you'd return Mistral API keys (or, better, a
   short-lived JWT scoped to your gateway).

### Pros

- **You own the stack end-to-end.** No xiaozhi protocol baggage.
- Lowest possible latency (one direct hop firmware ↔ Mistral).
- Free to re-design: e.g., always-on streaming with server-side VAD
  (skip wake-word), bidirectional realtime audio if Mistral exposes
  it.
- Frees you from `xiaozhi-esp32` upstream churn.

### Cons

- **Largest blast radius.** OTA flow, audio quality, AEC tuning,
  battery life, partition layout — all yours to verify.
- ESP32 development cycle (build → flash → test) is slow.
- You'll likely re-implement most of `audio_service.cc` even if you
  re-use the codec drivers.
- Updates require firmware OTA — slow rollout for fixes.

### Concrete files to touch

| Where | Change |
| --- | --- |
| `firmware/main/hal/board/hal_bridge.cc:99-109` | Replace `start_xiaozhi_app()` body with your own runtime |
| `firmware/main/hal/board/stackchan_display.cc` | Call avatar APIs from your state machine instead of xiaozhi `Application` |
| `firmware/main/hal/hal_mcp.cpp` | Swap MCP server registration for direct Mistral function dispatch |
| `firmware/main/apps/app_ai_agent/app_ai_agent.cpp:42` | Repoint or rename (cosmetic) |
| `firmware/main/CMakeLists.txt` | Drop `protocols/*` from xiaozhi sources; keep audio/codec/processor sources |
| New: `firmware/main/mistral/{client.cc, audio_loop.cc, state_machine.cc, tools.cc}` | Your own runtime |
| `firmware/repos.json` | Optional: drop `xiaozhi-esp32` clone if you've vendored only what you need |

## Hybrid you should probably consider

In practice the cleanest middle ground is:

> **"Path A first, then optionally Path B for future-proofing."**

- Ship Path A (gateway) end-to-end. You learn the wire protocol, you
  validate Mistral STT/TTS quality, you verify MCP-as-function-calls
  works on your model.
- Once stable, decide whether the protocol-translation cost is worth
  paying long-term. If not, attack Path B incrementally — swap
  `Protocol` first, then `Application`.

This staged approach gets you to a working Mistral-powered StackChan in
weeks rather than months, and de-risks the embedded work.

## Decision checklist

Pick **Path A** if any of these hold:

- You want a working demo soon.
- You don't want to maintain the firmware long-term.
- You expect to keep upgrading `xiaozhi-esp32` (for audio improvements,
  new boards, etc.).
- Your team has more server expertise than embedded C++.

Pick **Path B** if any of these hold:

- Latency / privacy / offline-mode is critical.
- You want a fully Mistral-branded firmware with no xiaozhi traces.
- You're prepared to own the full embedded stack including OTA.
- You'd rather not introduce a new always-on backend service.

## Cross-cutting concerns regardless of path

| Concern | Notes |
| --- | --- |
| **MCP tools** | Today: `self.robot.get_head_angles`, `self.robot.set_head_angles` (`hal_mcp.cpp`). Map to Mistral function calling — same JSON Schema shape. Add more tools (`set_emotion`, `play_dance`, `get_battery`) by extending `hal_mcp.cpp` patterns. |
| **Wake-word** | Stays on-device (`esp-sr` AFE model). If you switch to always-on streaming with server-side VAD, you can disable the wake-word component to save flash + RAM. |
| **Opus** | Firmware uses `espressif/esp_audio_codec`. If your gateway needs opus, use `github.com/hraban/opus` (CGo + libopus). Mistral audio APIs likely accept WAV/MP3/Opus — confirm formats. |
| **Agent config** | Today persisted in xiaozhi.me; mirrored in Go server's `service/agent.go`. For either path, persist locally and have your Mistral plane read it per session. |
| **License / activation** | `internal/controller/xiaozhi/xiaozhi_v1_get_xiao_zhi_generate_license_token.go` — this is xiaozhi-specific. Replace with your own provisioning, or stub. |
| **Companion plane** | `/stackChan/ws` between firmware and Go server (Opus/JPEG/control for the App) is unaffected. Keep it. |
| **OTA firmware updates** | xiaozhi's `ota.cc` handles firmware OTA in addition to server discovery. If you keep xiaozhi vendored (Path A), OTA still works. If you fully replace (Path B), reimplement OTA via `firmware/main/hal/hal_ota.cpp`. |

## Quick reference — all swap points in one table

| # | File | Path A touches? | Path B touches? |
| --- | --- | --- | --- |
| 1 | `firmware/main/hal/board/hal_bridge.cc:99-109` | No | **Yes** (replace) |
| 2 | `firmware/main/Kconfig.projbuild:5` (`OTA_URL`) | **Yes** (redirect) | Maybe (point to your provisioning) |
| 3 | `firmware/xiaozhi-esp32/main/protocols/*` | No (gateway speaks the protocol) | **Yes** (drop / rewrite) |
| 4 | `firmware/xiaozhi-esp32/main/audio/audio_service.cc` | No | Maybe (likely re-used as-is) |
| 5 | `firmware/main/hal/hal_mcp.cpp` | No | **Yes** (swap dispatch) |
| 6 | `firmware/main/hal/hal_ws_avatar.cpp` | No | No |
| 7 | `firmware/xiaozhi-esp32/main/audio/wake_words/*` | No | Maybe (drop if always-on) |
| 8 | `firmware/main/apps/app_ai_agent/app_ai_agent.cpp:42` | No | Cosmetic |
| 9 | `firmware/main/hal/board/stackchan_display.cc` | No | **Yes** (call from your loop) |
| 10 | `server/internal/web_socket/web_socket.go` (Opus / TextMessage cases) | **Yes** (new AI branch) | No |
| 11 | `server/internal/cmd/cmd.go:44` (route bind) | **Yes** (add `/stackChan/ai`) | No |
| 12 | `server/internal/xiaozhi/xiaozhi.go:37` baseUrl | **Yes** (stub or re-target) | Maybe |
| 13 | `server/internal/controller/xiaozhi/xiaozhi_v1_get_xiao_zhi_token.go` | **Yes** (return your JWT) | Maybe |
| 14 | `server/go.mod` (add opus, mistral SDK) | **Yes** | No |
| 15 | `app/lib/util/XiaoZhi_util.dart:38` baseUrl | Optional | Optional |
| 16 | `app/lib/util/XiaoZhi_util.dart:171-720` endpoint paths | Optional | Optional |
