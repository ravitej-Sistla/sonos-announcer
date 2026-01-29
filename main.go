package main

import (
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

//go:embed swagger.yaml
var swaggerSpec []byte

// SonosSpeaker represents a discovered Sonos speaker.
type SonosSpeaker struct {
	Name     string
	ID       string
	Location string // base URL e.g. http://192.168.1.10:1400
}

var (
	speakers   map[string]*SonosSpeaker
	speakersMu sync.RWMutex
	localIP    string
)

func main() {
	os.MkdirAll("./tts", 0755)

	localIP = getLocalIP()
	log.Printf("Local IP: %s", localIP)

	speakers = discoverSonos()
	logSpeakers()

	go startFileServer(localIP)
	go startAPIServer(localIP)

	log.Println("Sonos Gateway Ready")
	startTelegramBot()
}

// --------------- Network helpers ---------------

func getLocalIP() string {
	if ip := os.Getenv("LOCAL_IP"); ip != "" {
		return ip
	}
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal("Failed to detect local IP:", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// --------------- SSDP / UPnP Discovery ---------------

type deviceDescription struct {
	XMLName xml.Name `xml:"root"`
	Device  struct {
		RoomName    string `xml:"roomName"`
		DisplayName string `xml:"displayName"`
		ModelName   string `xml:"modelName"`
	} `xml:"device"`
}

func discoverSonos() map[string]*SonosSpeaker {
	result := make(map[string]*SonosSpeaker)

	ssdpAddr := "239.255.255.250:1900"
	searchTarget := "urn:schemas-upnp-org:device:ZonePlayer:1"

	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 3\r\n" +
		"ST: " + searchTarget + "\r\n" +
		"\r\n"

	addr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		log.Printf("SSDP resolve error: %v", err)
		return result
	}

	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		log.Printf("SSDP listen error: %v", err)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.WriteToUDP([]byte(msg), addr)

	locations := make(map[string]bool)
	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		for _, line := range strings.Split(string(buf[:n]), "\r\n") {
			upper := strings.ToUpper(line)
			if strings.HasPrefix(upper, "LOCATION:") {
				loc := strings.TrimSpace(line[len("LOCATION:"):])
				locations[loc] = true
			}
		}
	}

	for loc := range locations {
		if s := fetchSpeakerInfo(loc); s != nil {
			result[s.ID] = s
		}
	}
	return result
}

func fetchSpeakerInfo(location string) *SonosSpeaker {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(location)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var desc deviceDescription
	if err := xml.Unmarshal(body, &desc); err != nil {
		return nil
	}

	roomName := desc.Device.RoomName
	if roomName == "" {
		roomName = desc.Device.DisplayName
	}
	if roomName == "" {
		return nil
	}

	id := strings.ToLower(strings.ReplaceAll(roomName, " ", ""))

	// Extract base URL: http://host:port
	baseURL := location
	// Skip past "http://" (7 chars) or "https://" (8 chars) then find next "/"
	schemeEnd := strings.Index(location, "://")
	if schemeEnd >= 0 {
		rest := location[schemeEnd+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
			baseURL = location[:schemeEnd+3+slashIdx]
		}
	}

	return &SonosSpeaker{
		Name:     roomName,
		ID:       id,
		Location: baseURL,
	}
}

func logSpeakers() {
	fmt.Println("Discovered Sonos Speakers:")
	if len(speakers) == 0 {
		fmt.Println("  (none found)")
		return
	}
	for _, s := range speakers {
		fmt.Printf("- %s (id: %s)\n", s.Name, s.ID)
	}
}

// --------------- Text-to-Speech ---------------

func generateTTS(text string) (string, error) {
	filename := fmt.Sprintf("%d", time.Now().UnixNano())
	aiffPath := filepath.Join("tts", filename+".aiff")
	mp3Path := filepath.Join("tts", filename+".mp3")

	// Generate AIFF using macOS say
	if err := exec.Command("say", "-o", aiffPath, text).Run(); err != nil {
		return "", fmt.Errorf("say failed: %w", err)
	}

	// Convert AIFF -> MP3
	if err := exec.Command("afconvert", "-f", "mp3 ", "-d", ".mp3", aiffPath, mp3Path).Run(); err != nil {
		// Fallback: try AAC if MP3 encoding is unavailable
		mp3Path = filepath.Join("tts", filename+".m4a")
		if err2 := exec.Command("afconvert", "-f", "mp4f", "-d", "aac", aiffPath, mp3Path).Run(); err2 != nil {
			return "", fmt.Errorf("afconvert failed (mp3: %v, aac: %v)", err, err2)
		}
	}

	os.Remove(aiffPath)
	return mp3Path, nil
}

// --------------- Sonos Playback ---------------

func speak(text, target string) error {
	mp3Path, err := generateTTS(text)
	if err != nil {
		return err
	}

	mp3URL := fmt.Sprintf("http://%s:8080/%s", localIP, mp3Path)

	speakersMu.RLock()
	defer speakersMu.RUnlock()

	if target == "" || target == "all" {
		var lastErr error
		for _, s := range speakers {
			if err := playSonos(s, mp3URL); err != nil {
				log.Printf("Error playing on %s: %v", s.Name, err)
				lastErr = err
			}
		}
		return lastErr
	}

	s, ok := speakers[target]
	if !ok {
		return fmt.Errorf("speaker %q not found", target)
	}
	return playSonos(s, mp3URL)
}

func playSonos(speaker *SonosSpeaker, mediaURL string) error {
	controlURL := speaker.Location + "/MediaRenderer/AVTransport/Control"

	// SetAVTransportURI
	setURIBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"
 s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:SetAVTransportURI xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">
      <InstanceID>0</InstanceID>
      <CurrentURI>` + xmlEscape(mediaURL) + `</CurrentURI>
      <CurrentURIMetaData></CurrentURIMetaData>
    </u:SetAVTransportURI>
  </s:Body>
</s:Envelope>`

	if err := soapCall(controlURL, "SetAVTransportURI", setURIBody); err != nil {
		return fmt.Errorf("SetAVTransportURI: %w", err)
	}

	// Small delay to let Sonos buffer
	time.Sleep(300 * time.Millisecond)

	// Play
	playBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"
 s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:Play xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">
      <InstanceID>0</InstanceID>
      <Speed>1</Speed>
    </u:Play>
  </s:Body>
</s:Envelope>`

	if err := soapCall(controlURL, "Play", playBody); err != nil {
		return fmt.Errorf("Play: %w", err)
	}

	return nil
}

func soapCall(url, action, body string) error {
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", "urn:schemas-upnp-org:service:AVTransport:1#"+action)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SOAP %s returned %d: %s", action, resp.StatusCode, string(respBody))
	}
	return nil
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// --------------- File Server (port 8080) ---------------

func startFileServer(ip string) {
	addr := ip + ":8080"
	log.Printf("Starting TTS file server on %s", addr)
	if err := http.ListenAndServe(addr, http.FileServer(http.Dir("."))); err != nil {
		log.Fatalf("File server error: %v", err)
	}
}

// --------------- API Server (port 9000) ---------------

type speakerJSON struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type speakersResponse struct {
	Speakers []speakerJSON `json:"speakers"`
}

type speakRequest struct {
	Text   string `json:"text"`
	Target string `json:"target"`
}

func startAPIServer(ip string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/speakers", handleSpeakers)
	mux.HandleFunc("/speak", handleSpeak)
	mux.HandleFunc("/swagger.yaml", handleSwaggerSpec)
	mux.HandleFunc("/swagger/", handleSwaggerUI)

	addr := ip + ":9000"
	log.Printf("Starting API server on %s", addr)
	log.Printf("Swagger UI available at http://%s:9000/swagger/", ip)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("API server error: %v", err)
	}
}

func handleSwaggerSpec(w http.ResponseWriter, r *http.Request) {
	spec := strings.ReplaceAll(string(swaggerSpec), "http://localhost:9000", "http://"+localIP+":9000")
	w.Header().Set("Content-Type", "application/yaml")
	w.Write([]byte(spec))
}

func handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Sonos Gateway - Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>html { box-sizing: border-box; } body { margin: 0; }</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: "/swagger.yaml",
      dom_id: "#swagger-ui",
      presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
      layout: "BaseLayout"
    });
  </script>
</body>
</html>`)
}

func handleSpeakers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	speakersMu.RLock()
	defer speakersMu.RUnlock()

	resp := speakersResponse{Speakers: make([]speakerJSON, 0, len(speakers))}
	for _, s := range speakers {
		resp.Speakers = append(resp.Speakers, speakerJSON{Name: s.Name, ID: s.ID})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleSpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req speakRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Text == "" {
		http.Error(w, `"text" is required`, http.StatusBadRequest)
		return
	}

	target := req.Target
	if target == "" {
		target = "all"
	}

	if err := speak(req.Text, target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --------------- Telegram Bot ---------------

func startTelegramBot() {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Println("TELEGRAM_BOT_TOKEN not set, Telegram bot disabled")
		select {} // block forever so the process stays alive
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Telegram bot init error: %v", err)
	}
	log.Printf("Telegram bot authorized as @%s", bot.Self.UserName)

	allowedUser := int64(0)
	if v := os.Getenv("ALLOWED_TELEGRAM_USER"); v != "" {
		allowedUser, _ = strconv.ParseInt(v, 10, 64)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		// Ignore messages from unauthorized users
		if allowedUser != 0 && update.Message.From.ID != allowedUser {
			continue
		}

		text := strings.TrimSpace(update.Message.Text)
		if text == "" {
			continue
		}

		if text == "/speakers" || text == "/speakers@"+bot.Self.UserName {
			handleTelegramSpeakers(bot, update.Message.Chat.ID)
			continue
		}

		// Skip other bot commands
		if strings.HasPrefix(text, "/") {
			continue
		}

		handleTelegramAnnouncement(bot, update.Message.Chat.ID, text)
	}
}

func handleTelegramSpeakers(bot *tgbotapi.BotAPI, chatID int64) {
	speakersMu.RLock()
	defer speakersMu.RUnlock()

	if len(speakers) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "No Sonos speakers found."))
		return
	}

	var sb strings.Builder
	sb.WriteString("Available Sonos Speakers:\n\n")
	for _, s := range speakers {
		fmt.Fprintf(&sb, "\u2022 %s \u2192 id: %s\n", s.Name, s.ID)
	}
	sb.WriteString("\nSend:\nkitchen: Dinner is ready\nOR just:\nDinner is ready")

	bot.Send(tgbotapi.NewMessage(chatID, sb.String()))
}

func handleTelegramAnnouncement(bot *tgbotapi.BotAPI, chatID int64, text string) {
	target := "all"
	message := text

	// If message contains ":" the left side is the target speaker
	if idx := strings.Index(text, ":"); idx > 0 {
		candidate := strings.TrimSpace(text[:idx])
		// Normalize candidate the same way speaker IDs are normalized
		candidateID := strings.ToLower(strings.ReplaceAll(candidate, " ", ""))

		speakersMu.RLock()
		_, exists := speakers[candidateID]
		speakersMu.RUnlock()

		if exists {
			target = candidateID
			message = strings.TrimSpace(text[idx+1:])
		}
	}

	if message == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "Empty announcement text."))
		return
	}

	log.Printf("Announcement: %q -> %s", message, target)

	if err := speak(message, target); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "Error: "+err.Error()))
		return
	}

	reply := fmt.Sprintf("Announced on %s: %s", target, message)
	bot.Send(tgbotapi.NewMessage(chatID, reply))
}
