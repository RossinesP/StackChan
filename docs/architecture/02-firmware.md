# 02 — Firmware (ESP32-S3)

## Big picture

The firmware is **two stacked runtimes** that take turns owning the CPU:

```
┌──────────────────────────────────────────────────────────────┐
│                     ESP32-S3 (CoreS3)                        │
│                                                              │
│  Boot → Mooncake UI runtime ─── user opens "AI.AGENT" ───┐   │
│         (launcher, apps, HAL,                            │   │
│          avatar, dance, setup)                           ▼   │
│                                                                │
│                           teardown                             │
│                              │                                 │
│                              ▼                                 │
│                  xiaozhi-esp32 runtime  (forever)             │
│                  (Application, AudioService, Protocol,        │
│                   McpServer, Display)                         │
└──────────────────────────────────────────────────────────────┘
```

Reference: `firmware/main/main.cpp` (full file is ~57 lines).

```cpp
// main.cpp:23-39  install Mooncake apps
GetHAL().init();
GetMooncake().installApp(std::make_unique<AppLauncher>());
GetMooncake().installApp(std::make_unique<AppAiAgent>());     // ← the AI tile
GetMooncake().installApp(std::make_unique<AppAvatar>());
... (5 more)

// main.cpp:42-50  loop until the AI tile is opened
while (1) {
    GetHAL().feedTheDog();
    GetMooncake().update();
    if (GetHAL().isXiaozhiStartRequested()) break;     // ← single trigger
}

// main.cpp:53-56  hand control to xiaozhi forever
GetMooncake().uninstallAllApps();
DestroyMooncake();
GetHAL().startXiaozhi();   // never returns
```

## Mooncake apps

Source: `firmware/main/apps/`

| App | Role |
| --- | --- |
| `app_launcher` | Home screen, paginated icon grid |
| **`app_ai_agent`** | **3-line stub** — flips the flag that swaps to xiaozhi runtime |
| `app_avatar` | Stack-Chan avatar UI driven by HAL signals (calls, dance, video, text) |
| `app_dance` | Choreography demo |
| `app_app_center` | Browse/install OTA mini-apps |
| `app_ezdata` | M5 EzData cloud pairing (QR) |
| `app_espnow_ctrl` | ESP-NOW remote pairing |
| `app_setup` | First-boot wizard (servo cal, WiFi, brightness, factory reset) |
| `app_template` | Skeleton |
| `common/` | Shared widgets (status_bar, toast, loading, reminder, ...) |

### `app_ai_agent` — the entire AI tile

`firmware/main/apps/app_ai_agent/app_ai_agent.h` — 22 lines, defines
`AppAiAgent : mooncake::AppAbility`.

`firmware/main/apps/app_ai_agent/app_ai_agent.cpp` — 57 lines, the only
real action is in `onOpen()`:

```cpp
void AppAiAgent::onOpen() {
    GetHAL().requestXiaozhiStart();   // ← that's it
}
```

Every later swap of "the AI" must redirect this single call (or the
`startXiaozhi()` it eventually triggers).

## Hardware Abstraction Layer

Source: `firmware/main/hal/`

| File | Responsibility |
| --- | --- |
| `hal.{h,cpp}` | `Hal` singleton; orchestrates all sub-modules; owns `uitk::Signal<...>` cross-app events |
| **`board/hal_bridge.{h,cc}`** | **Glue to xiaozhi runtime.** `start_xiaozhi_app()` calls `Application::Initialize() + Run()` |
| `board/stackchan.cc` | Custom xiaozhi `Board` subclass for power/LCD/touch on this board |
| `board/stackchan_display.{h,cc}` | Custom xiaozhi `Display` overriding `SetEmotion`/`SetChatMessage` to drive the StackChan avatar |
| `hal_ws_avatar.cpp` | **Companion-server WS client** — separate from xiaozhi; Opus/JPEG/control bridge to phone via Go server |
| `hal_mcp.cpp` | Registers MCP tools (`self.robot.get_head_angles`, `set_head_angles`) into xiaozhi's `McpServer` |
| `hal_network.cpp` | WiFi/cellular bring-up wrapping xiaozhi `WifiBoard`/`Ml307Board` |
| `hal_ble.cpp` | NimBLE GATT server for app-side provisioning |
| `hal_servo.cpp` | Servo angle control with safety limits |
| `hal_head_touch.cpp` | Head touch / swipe gesture detection (Si12T) |
| `hal_imu.cpp` | BMI270 IMU; emits Shake/PickUp `ImuMotionEvent` |
| `hal_io_expander.cpp` | PY32 IO expander |
| `hal_rtc.cpp` | PCF8563 RTC + timezone sync |
| `hal_ota.cpp` | Firmware OTA download/apply |
| `hal_app_center.cpp` | Mini-app catalog HTTP API |
| `hal_account.cpp` | User account binding |
| `hal_ezdata.cpp` | M5 EzData pairing-code service |
| `hal_espnow.cpp` | ESP-NOW peer discovery + send/receive |
| `drivers/{PY32IOExpander,PCF8563,bmi270,Si12T}/` | Low-level device drivers |
| `utils/wifi_connect/` | Simplified WiFi station for config-mode validation |
| `utils/bleprph/` | NimBLE peripheral skeleton |
| `utils/jpeg_to_image/` | JPEG → LVGL RGB565 decoder for inbound video frames |
| `utils/motion_detector/` | Shake detector on IMU |
| `utils/secret_logic/` | **Companion-server URL + auth token generator** (default `http://localhost:3000`, weak-symbol overridable) |
| `utils/ota/` | OTA helper |

## `stackchan/` library — avatar, motion, decorators

Source: `firmware/main/stackchan/` — pure C++, no network, no AI.

```
stackchan.{h,cpp}              StackChan : Modifiable — singleton, update loop
modifiable.h                   Modifiable / Modifier base classes (decorator pattern)
avatar/                        Avatar (eyes, mouth, speech bubble) drawn via LVGL
motion/                        Servo motion (yaw/pitch, auto-sync, torque release)
animation/                     parse_sequence_from_json — dance sequence parser
addons/neon_light/             RGB LED strip wrapper
modifiers/                     TimedSpeechModifier, SpeakingModifier,
                                TimedEmotionModifier, DanceModifier
utils/object_pool.h            Auto-cleanup of decorators/modifiers
```

Exposes `GetStackChan().avatar()`, `.motion()`, `.addModifier()`,
`.updateAvatarFromJson()`, `.updateMotionFromJson()`.

## The vendored xiaozhi-esp32

`firmware/repos.json` clones `https://github.com/78/xiaozhi-esp32.git` at
tag `v2.2.4` into `firmware/xiaozhi-esp32/`, with a local patch at
`firmware/patches/xiaozhi-esp32.patch` (touches Activation flow + disables
some EmoteDisplay assets).

`firmware/main/CMakeLists.txt` (lines ~23-63) compiles **selected
xiaozhi-esp32 sources directly into the firmware binary** (not as a
component):

| Concern | xiaozhi-esp32 source |
| --- | --- |
| Main app loop / state machine | `application.cc`, `device_state_machine.cc` |
| Audio service (mic↔speaker pipelines) | `audio/audio_service.cc`, `audio/audio_codec.cc` |
| Audio codecs (chip drivers) | `audio/codecs/{box,es8311,es8374,es8388,es8389,no,dummy}_audio_codec.cc` |
| Opus encode/decode | via `espressif/esp_audio_codec` component |
| OGG demuxer | `audio/demuxer/ogg_demuxer.cc` |
| Audio processor (AEC/NS) | `audio/processors/{afe,no}_audio_processor.cc` |
| **Wake word** | `audio/wake_words/{afe,custom,esp}_wake_word.cc` (uses `espressif/esp-sr`) |
| **AI transport** | `protocols/protocol.cc`, `websocket_protocol.cc`, `mqtt_protocol.cc` |
| **MCP server** | `mcp_server.cc` |
| OTA / activation | `ota.cc` |
| Display / theme / GIF / JPEG | `display/lcd_display.cc`, `display/lvgl_display/*` |

Default LLM/voice server endpoint is set in
`firmware/main/Kconfig.projbuild:5`:

```
config OTA_URL
    default "https://api.tenclass.net/xiaozhi/ota/"
```

The OTA endpoint returns the WebSocket / MQTT URL the device then connects
to for live audio.

## AI/voice components inventory

The firmware runs the entire local half of the audio loop:

| Layer | Where | Notes |
| --- | --- | --- |
| Mic / speaker I2S | xiaozhi `audio/codecs/*` | Per-board chip driver |
| AEC + Noise Suppression + VAD | xiaozhi `audio/processors/afe_audio_processor.cc` + `esp-sr` | Local DSP |
| Wake-word | xiaozhi `audio/wake_words/*` + `esp-sr` model | Local |
| Opus codec | `espressif/esp_audio_codec` ~2.4.1 | Hardware-accelerated |
| Voice transport | xiaozhi `protocols/websocket_protocol.cc` (or `mqtt_protocol.cc`) | **The Mistral seam** |
| MCP tool dispatch | xiaozhi `mcp_server.cc` + `firmware/main/hal/hal_mcp.cpp` | Tools registered locally; LLM invokes them remotely |

The device is a **thin client**: capture → wake-word → opus → WS/MQTT to
xiaozhi server → receive opus + JSON → play + animate. **STT, LLM, TTS
are server-side.**

## [MISTRAL] Swap-point map

Ordered from least to most invasive — see
[`06-mistral-migration.md`](./06-mistral-migration.md) for the full plan.

| # | Where | What it currently does |
| --- | --- | --- |
| 1 | `firmware/main/hal/board/hal_bridge.cc:99-109` (`start_xiaozhi_app`) | Single function that hands control to xiaozhi forever. **Highest leverage swap point.** |
| 2 | `firmware/main/Kconfig.projbuild:5` (`OTA_URL`) | Server-discovery URL; redirect to your own gateway speaking xiaozhi's protocol = minimal firmware change |
| 3 | `firmware/xiaozhi-esp32/main/protocols/{websocket,mqtt}_protocol.cc` | Wire format for the AI plane. Replace with `mistral_protocol.cc` and register in `Application::InitializeProtocol()` |
| 4 | `firmware/xiaozhi-esp32/main/audio/audio_service.cc` | Owns mic→opus→protocol and protocol→opus→speaker pipelines |
| 5 | `firmware/main/hal/hal_mcp.cpp` | Tool registration; map to Mistral function-calling JSON schema |
| 6 | `firmware/main/hal/hal_ws_avatar.cpp` + `utils/secret_logic/` | Companion-server WS (independent of AI plane); could host a Mistral pipeline |
| 7 | `firmware/xiaozhi-esp32/main/audio/wake_words/*` + `esp-sr` | Local wake word — keep, or replace with always-on streaming |
| 8 | `firmware/main/apps/app_ai_agent/app_ai_agent.cpp:42` | The launcher tile entry; trivial to fork |
| 9 | `firmware/main/hal/board/stackchan_display.cc` | xiaozhi `Display` overrides driving the avatar; call directly if bypassing xiaozhi `Application` |
