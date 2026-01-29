You are building a macOS-based Golang service that works as a voice announcement gateway for Sonos speakers.

The system receives text from Telegram messages or an HTTP API, converts it to speech, and plays the announcement on one or all Sonos speakers in the house.

🎯 PROJECT GOAL

Create a Go application that:

Discovers Sonos speakers on the local WiFi network

Lets the user list available speakers

Accepts text announcements

Converts text → speech (MP3) using macOS built-in TTS

Hosts the MP3 via local HTTP

Plays the announcement on:

A specific speaker

OR all speakers (default)

🧱 TECH STACK

Language: Go

Libraries:

Sonos control
github.com/ianr0bkny/go-sonos

Telegram Bot
github.com/go-telegram-bot-api/telegram-bot-api/v5

Standard Go libs for HTTP server, JSON, file serving, and os/exec

🖥 ENVIRONMENT

Runs on macOS on the same WiFi network as Sonos speakers.

Use built-in macOS TTS:

say -o tts.aiff "Dinner is ready"
afconvert -f mp3 -d mp3 tts.aiff tts.mp3


Claude must implement this via Go’s exec.Command.

🔍 SONOS DISCOVERY

On startup:

Discover all Sonos devices

Get each speaker’s Room Name

Normalize it into an ID:

Normalization rules:

lowercase

remove spaces

Example:

Room Name	ID
Living Room	livingroom
Kitchen	kitchen

Store in:

map[string]*sonos.Device


Log discovered speakers like:

Discovered Sonos Speakers:
- Living Room (id: livingroom)
- Kitchen (id: kitchen)

🔊 TEXT-TO-SPEECH FLOW

When an announcement is triggered:

Create unique filename (timestamp or UUID)

Run say to generate AIFF

Convert AIFF → MP3 using afconvert

Save MP3 in ./tts/

🌐 LOCAL FILE SERVER

Start HTTP server on port 8080 serving ./tts

Example playable URL:

http://<mac_local_ip>:8080/tts/<file>.mp3


Claude must auto-detect the Mac’s local IP.

📢 PLAYBACK LOGIC

Implement:

func speak(text string, target string)


Behavior:

If target == "" OR "all"

Play on all discovered speakers

If target == "<speakerID>"

Play only on that speaker

Use Sonos AVTransport SetAVTransportURI to play the MP3 URL.

🤖 TELEGRAM BOT FEATURES

Bot token from ENV:

TELEGRAM_BOT_TOKEN


Allowed user ID from ENV:

ALLOWED_TELEGRAM_USER


Ignore messages from other users.

Telegram Commands
1️⃣ List speakers

User sends:

/speakers


Bot replies with:

Available Sonos Speakers:

• Living Room → id: livingroom
• Kitchen → id: kitchen

Send:
kitchen: Dinner is ready
OR just:
Dinner is ready

2️⃣ Announcements

All speakers (default):

Dinner is ready


Specific speaker:

kitchen: Dinner is ready
livingroom: Movie time


Parsing rule:

If message contains : → left side = target speaker

Else → target = "all"

🌍 HTTP API
GET /speakers

Returns:

{
  "speakers": [
    {"name": "Living Room", "id": "livingroom"},
    {"name": "Kitchen", "id": "kitchen"}
  ]
}

POST /speak

Request:

{
  "text": "Dinner is ready",
  "target": "kitchen"
}


Rules:

If target missing → default to "all"

If target == "all" → broadcast to all speakers

📁 PROJECT STRUCTURE
/sonos-gateway
  main.go
  go.mod
  /tts   (auto-created if missing)

▶️ APP STARTUP FLOW

Discover Sonos speakers

Log discovered speakers

Start TTS file server (8080)

Start API server (9000)

Start Telegram bot listener

Log “Sonos Gateway Ready”

🧪 TEST SCENARIOS

Claude must ensure:

✅ /speakers in Telegram returns list
✅ “Dinner is ready” → plays on ALL speakers
✅ “kitchen: Hello” → plays only in kitchen
✅ HTTP /speakers returns JSON list
✅ HTTP POST /speak triggers playback
✅ MP3 file accessible in browser

🚫 DO NOT

No cloud TTS

No database

No Docker

No frontend UI

🧠 CLAUDE OUTPUT REQUIRED

Claude must generate:

Full main.go (complete, runnable)

go.mod

Instructions to run:

Set Telegram bot token

Find Telegram user ID

Run server

End of build instructions. Implement the full working project.
