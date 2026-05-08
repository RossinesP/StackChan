#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
# SPDX-License-Identifier: MIT
#
# dev-gateway.sh — start the Mistral gateway with LAN IP auto-detected,
# and print the exact firmware flash instructions for a real StackChan.
#
# Usage:
#   ./scripts/dev-gateway.sh                # auto-detect IP, run server
#   IP=192.168.1.42 ./scripts/dev-gateway.sh   # override IP
#   PORT=12800 ./scripts/dev-gateway.sh        # override port
#
# See docs/architecture/08-local-dev-setup.md for the full setup.

set -euo pipefail

PORT="${PORT:-12800}"

# ----- Locate repo + server dir ----------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$SERVER_DIR"

# ----- Pre-flight checks ------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: 'go' not found in PATH. Install Go 1.24+ from https://golang.google.cn/dl/" >&2
  exit 1
fi

if ! command -v openssl >/dev/null 2>&1; then
  echo "ERROR: 'openssl' not found in PATH. Needed once to generate dev RSA keys." >&2
  exit 1
fi

if lsof -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "ERROR: port $PORT already in use." >&2
  lsof -iTCP:"$PORT" -sTCP:LISTEN >&2
  echo >&2
  echo "Kill it with:" >&2
  echo "  kill \$(lsof -tiTCP:$PORT -sTCP:LISTEN)" >&2
  echo "Or run on a different port:  PORT=12801 $0" >&2
  exit 1
fi

# ----- Dev config (RSA keys + JWT) -------------------------------------------
# server/utility/rsa.go panics if RSA keys are missing from config. The tracked
# manifest/config/config.yaml ships with empty keys, so we generate a one-off
# dev config that GoFrame loads via GF_GCFG_FILE. Idempotent: keys persist
# across runs once generated.
DEV_DIR="${DEV_DIR:-$HOME/.stackchan-dev}"
DEV_CONFIG="$DEV_DIR/server-config.yaml"
mkdir -p "$DEV_DIR"

ensure_dev_config() {
  if [[ -f "$DEV_CONFIG" ]]; then
    return
  fi
  echo "Generating dev RSA keys + JWT secret in $DEV_DIR (one-time setup)..."

  local server_priv server_pub client_priv client_pub jwt_secret
  server_priv="$(openssl genrsa 2048 2>/dev/null)"
  server_pub="$(echo "$server_priv" | openssl rsa -pubout 2>/dev/null)"
  client_priv="$(openssl genrsa 2048 2>/dev/null)"
  client_pub="$(echo "$client_priv" | openssl rsa -pubout 2>/dev/null)"
  jwt_secret="$(openssl rand -hex 32)"

  # YAML literal block (|) preserves PEM newlines as-is.
  {
    cat "$SERVER_DIR/manifest/config/config.yaml" \
      | sed -E 's|^(jwt:.*$)|# &|' \
      | sed -E 's|^(rsa:.*$)|# &|' \
      | sed -E 's|^(  secret:.*$)|# &|' \
      | sed -E 's|^(  server:.*$)|# &|' \
      | sed -E 's|^(  client:.*$)|# &|' \
      | sed -E 's|^(    public:.*$)|# &|' \
      | sed -E 's|^(    private:.*$)|# &|'
    echo
    echo "# --- dev overrides written by scripts/dev-gateway.sh ---"
    echo "jwt:"
    echo "  secret: \"$jwt_secret\""
    echo "rsa:"
    echo "  server:"
    echo "    public: |"
    echo "$server_pub"   | sed 's/^/      /'
    echo "    private: |"
    echo "$server_priv"  | sed 's/^/      /'
    echo "  client:"
    echo "    public: |"
    echo "$client_pub"   | sed 's/^/      /'
    echo "    private: |"
    echo "$client_priv"  | sed 's/^/      /'
  } > "$DEV_CONFIG"

  echo "Wrote $DEV_CONFIG (keys persist across runs; delete to regenerate)."
}

ensure_dev_config

# ----- Detect LAN IP ----------------------------------------------------------
detect_ip() {
  if [[ -n "${IP:-}" ]]; then
    echo "$IP"
    return
  fi
  case "$(uname -s)" in
    Darwin)
      # Try wifi first (en0), then ethernet (en1), then any non-loopback IPv4.
      for iface in en0 en1 en2; do
        ip="$(ipconfig getifaddr "$iface" 2>/dev/null || true)"
        if [[ -n "$ip" ]]; then
          echo "$ip"
          return
        fi
      done
      ;;
    Linux)
      ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -1
      return
      ;;
  esac
  # Last-ditch fallback
  hostname -I 2>/dev/null | awk '{print $1}' || echo ""
}

LAN_IP="$(detect_ip)"

if [[ -z "$LAN_IP" ]]; then
  echo "ERROR: could not auto-detect LAN IP. Set IP=<your laptop IP> and re-run." >&2
  exit 1
fi

OTA_URL="http://${LAN_IP}:${PORT}/xiaozhi/ota/"
WS_URL="ws://${LAN_IP}:${PORT}/xiaozhi/v1/"

# ----- Banner -----------------------------------------------------------------
cat <<EOF

╭─ Mistral Gateway (Path A, milestone M1+M2) ──────────────────────────────────
│
│  LAN IP detected : ${LAN_IP}
│  Listening on    : 0.0.0.0:${PORT}
│  OTA endpoint    : ${OTA_URL}
│  WebSocket       : ${WS_URL}
│
╰─ Smoke-test from this laptop ────────────────────────────────────────────────

  curl -s -X POST ${OTA_URL} \\
    -H 'Device-Id: AA:BB:CC:DD:EE:FF' -d '{}' | jq

  echo '{"type":"hello","version":1,"transport":"websocket","features":{"mcp":true},"audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}' \\
    | websocat ${WS_URL}

╭─ One-time firmware flash (real StackChan) ───────────────────────────────────
│
│  Prerequisite — install ESP-IDF v5.5.4 (one-time, ~3 GB):
│
│    mkdir -p ~/esp && cd ~/esp
│    git clone -b v5.5.4 --recursive https://github.com/espressif/esp-idf.git
│    cd esp-idf && ./install.sh esp32s3
│
│  In every shell where you'll run idf.py, source the export script:
│
│    source ~/esp/esp-idf/export.sh
│
│  Then verify:  idf.py --version    (should print v5.5.4)
│
│  ── Flash steps ─────────────────────────────────────────────────────────────
│
│  1. Make sure the StackChan is on the SAME wifi as this laptop.
│
│  2. From repo root:
│       cd firmware
│       python3 ./fetch_repos.py        # first time only
│       source ~/esp/esp-idf/export.sh  # if not already sourced
│       idf.py set-target esp32s3       # first time only
│       idf.py menuconfig
│
│  3. Navigate: Xiaozhi Assistant → Default OTA URL
│     Set to:   ${OTA_URL}
│
│  4. Save & exit, then:
│       idf.py build flash monitor
│
│  5. Watch THIS terminal for:
│       ota request   device_id=...
│       ws upgrade    device_id=...
│       hello in      format=opus rate=16000 ...
│       hello-ack sent session_id=...
│
│  After that single flash you can iterate on the gateway freely —
│  no more reflashing needed unless you change CONFIG_OTA_URL itself.
│
╰─ Troubleshooting ────────────────────────────────────────────────────────────

  - "idf.py: command not found"        → source ~/esp/esp-idf/export.sh
  - Device gets "connection refused"   → macOS firewall: allow incoming on 'go'
                                          System Settings → Network → Firewall
  - LAN IP changes between sessions    → reserve in router DHCP, or re-run this
                                          script and reflash with the new URL
  - Firmware rejects plain HTTP        → fall back to a tunnel (see
                                          docs/architecture/08-local-dev-setup.md)

EOF

# ----- Run --------------------------------------------------------------------
export GATEWAY_WS_URL="$WS_URL"
export GATEWAY_OPUS_VERSION="${GATEWAY_OPUS_VERSION:-2}"

# M4+: Mistral API key for TTS / STT / chat. Without it, gateway falls
# back to M3 echo loopback (you hear yourself instead of a synthesized
# reply). Set via:  export MISTRAL_API_KEY=...
# Optional:
#   MISTRAL_TTS_MODEL    (default: voxtral-mini-tts-2603)
#   MISTRAL_TTS_VOICE    (default: auto-discover first voice from API;
#                          list with: curl -H "Authorization: Bearer \$MISTRAL_API_KEY" \
#                                      https://api.mistral.ai/v1/audio/voices | jq)
#   GATEWAY_TTS_REPLY    (default: static hello — only used if STT path
#                          is disabled or fails)
#   GATEWAY_TTS_PEAK     (default: 28000 ≈ -1.4 dBFS; 0 disables boost)
#
# M5 (transcribe-and-reply, default behaviour when API key is set):
#   MISTRAL_STT_MODEL    (default: voxtral-mini-latest)
#   MISTRAL_STT_LANGUAGE (default: empty = auto-detect; e.g. "en", "fr")
#   GATEWAY_STT_REPLY    (default: "You said: %s"; set to empty to fall
#                          back to M4 static greeting)

# Point GoFrame at the dev config so utility/rsa.go finds RSA keys.
export GF_GCFG_FILE="$DEV_CONFIG"
export GF_GCFG_PATH="$DEV_DIR"

echo "Env:"
echo "  GATEWAY_WS_URL       = $GATEWAY_WS_URL"
echo "  GATEWAY_OPUS_VERSION = $GATEWAY_OPUS_VERSION"
echo "  GF_GCFG_FILE         = $GF_GCFG_FILE"
if [[ -n "${MISTRAL_API_KEY:-}" ]]; then
  echo "  MISTRAL_API_KEY      = sk-***${MISTRAL_API_KEY: -4} (M4 TTS enabled)"
else
  echo "  MISTRAL_API_KEY      = (unset → M3 echo loopback)"
fi
echo "Press Ctrl-C to stop."
echo "──────────────────────────────────────────────────────────────────────────────"
echo

exec go run main.go
