# Sonos Speaker Gateway

A macOS-based Go service that works as a voice announcement gateway for Sonos speakers. It receives text from Telegram messages or an HTTP API, converts it to speech using macOS built-in TTS, and plays the announcement on one or all Sonos speakers on the local network.

## Prerequisites

- macOS (uses built-in `say` and `afconvert` commands)
- Go 1.21+
- Sonos speakers on the same WiFi network

## Build

```bash
go mod tidy
CGO_ENABLED=0 go build -o sonos-gateway .
```

## Configuration

Set the following environment variables:

| Variable | Required | Description |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | No | Telegram bot token from [@BotFather](https://t.me/BotFather). If not set, the Telegram bot is disabled but the HTTP API still works. |
| `ALLOWED_TELEGRAM_USER` | No | Telegram user ID to restrict bot access. If not set, the bot responds to all users. |
| `LOCAL_IP` | No | Override the auto-detected local IP address. Useful when the machine has multiple network interfaces. |

### Finding your Telegram user ID

Send a message to [@userinfobot](https://t.me/userinfobot) on Telegram to get your user ID.

## Run

```bash
export TELEGRAM_BOT_TOKEN="your-bot-token"
export ALLOWED_TELEGRAM_USER="your-telegram-user-id"
./sonos-gateway
```

On startup the service will:

1. Discover Sonos speakers on the local network
2. Start TTS file server on port **8080**
3. Start API server on port **9000**
4. Start Telegram bot listener (if token is set)

## Swagger UI

Interactive API documentation is available at:

```
http://localhost:9000/swagger/
```

The OpenAPI spec is served at `http://localhost:9000/swagger.yaml`.

## HTTP API

### List speakers

```
GET http://localhost:9000/speakers
```

Response:

```json
{
  "speakers": [
    {"name": "Living Room", "id": "livingroom"},
    {"name": "Kitchen", "id": "kitchen"}
  ]
}
```

### Send announcement

```
POST http://localhost:9000/speak
Content-Type: application/json

{
  "text": "Dinner is ready",
  "target": "kitchen"
}
```

- Omit `target` or set to `"all"` to play on all speakers.
- Set `target` to a speaker ID to play on a specific speaker.

## Telegram Bot

### Commands

- `/speakers` — List discovered Sonos speakers and their IDs.

### Announcements

Send a message to the bot:

- `Dinner is ready` — plays on **all** speakers
- `kitchen: Dinner is ready` — plays only on the **kitchen** speaker

## Testing with the Sonos Emulator

A lightweight Sonos speaker emulator is included in `emulator/` for testing without real hardware. It simulates SSDP discovery, UPnP device descriptions, and AVTransport SOAP control.

### Build the emulator

```bash
cd emulator
CGO_ENABLED=0 go build -o sonos-emulator .
```

### Emulator flags

| Flag | Default | Description |
|---|---|---|
| `-speakers` | `"Living Room,Kitchen"` | Comma-separated list of virtual speaker names |
| `-port` | `1400` | Starting HTTP port (increments per speaker) |
| `-verify` | `false` | Fetch the media URL on Play to verify it is accessible |
| `-play` | `false` | Download and play the TTS audio through Mac speakers using `afplay` |

### End-to-end test

```bash
# Terminal 1: Start the emulator
cd emulator
./sonos-emulator -speakers "Living Room,Kitchen,Bedroom" -play

# Terminal 2: Start the gateway
./sonos-gateway

# Terminal 3: Test
curl http://localhost:9000/speakers
curl -X POST http://localhost:9000/speak \
  -H 'Content-Type: application/json' \
  -d '{"text":"Hello","target":"kitchen"}'
```

The emulator logs all SOAP actions it receives, so you can verify the gateway sent the correct commands.

### Official Sonos Simulator

For more comprehensive testing, Sonos provides an official simulator at https://developer.sonos.com/tools/developer-tools/sonos-simulator/ (requires a Sonos developer account and Node.js).
