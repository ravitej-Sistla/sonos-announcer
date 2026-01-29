// Sonos Speaker Emulator
//
// A lightweight emulator that simulates Sonos speakers on the local network
// for testing the Sonos announcement gateway without real hardware.
//
// Supports SSDP discovery, UPnP device descriptions, and AVTransport SOAP control.
//
// For production testing with the official Sonos Simulator, see:
//   https://developer.sonos.com/tools/developer-tools/sonos-simulator/
//
// Usage:
//
//	go run main.go -speakers "Living Room,Kitchen,Bedroom" -verify
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type VirtualSpeaker struct {
	Name     string
	Port     int
	MediaURI string
}

var (
	speakersFlag = flag.String("speakers", "Living Room,Kitchen", "comma-separated list of virtual speaker names")
	basePort     = flag.Int("port", 1400, "starting HTTP port for the first speaker")
	verify       = flag.Bool("verify", false, "fetch the media URL on Play to verify accessibility")
	play         = flag.Bool("play", false, "download and play the TTS audio through Mac speakers using afplay")
)

func main() {
	flag.Parse()

	names := strings.Split(*speakersFlag, ",")
	speakers := make([]*VirtualSpeaker, 0, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		speakers = append(speakers, &VirtualSpeaker{
			Name: name,
			Port: *basePort + i,
		})
	}

	if len(speakers) == 0 {
		log.Fatal("No speakers configured")
	}

	localIP := getLocalIP()
	log.Printf("Local IP: %s", localIP)

	fmt.Println("Virtual Sonos Speakers:")
	for _, spk := range speakers {
		fmt.Printf("  - %s on port %d\n", spk.Name, spk.Port)
	}

	for _, spk := range speakers {
		go startSpeakerHTTP(spk)
	}

	go startSSDPResponder(speakers, localIP)

	log.Println("Sonos Emulator Ready")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down")
}

// --------------- Network helpers ---------------

func getLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal("Failed to detect local IP:", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// --------------- SSDP Responder ---------------

func startSSDPResponder(speakers []*VirtualSpeaker, localIP string) {
	addr := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.Fatalf("SSDP listen error: %v", err)
	}
	defer conn.Close()

	conn.SetReadBuffer(8192)
	log.Println("[SSDP] Listening on 239.255.255.250:1900")

	buf := make([]byte, 4096)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("[SSDP] Read error: %v", err)
			continue
		}

		msg := string(buf[:n])
		if !strings.Contains(msg, "M-SEARCH") {
			continue
		}
		if !strings.Contains(msg, "urn:schemas-upnp-org:device:ZonePlayer:1") {
			continue
		}

		log.Printf("[SSDP] M-SEARCH received from %s", remoteAddr)

		// Send unicast responses back to the gateway via a separate socket
		respConn, err := net.DialUDP("udp4", nil, remoteAddr)
		if err != nil {
			log.Printf("[SSDP] Failed to dial %s: %v", remoteAddr, err)
			continue
		}

		for _, spk := range speakers {
			location := fmt.Sprintf("http://%s:%d/xml/device_description.xml", localIP, spk.Port)
			response := "HTTP/1.1 200 OK\r\n" +
				"CACHE-CONTROL: max-age=1800\r\n" +
				"LOCATION: " + location + "\r\n" +
				"ST: urn:schemas-upnp-org:device:ZonePlayer:1\r\n" +
				"USN: uuid:RINCON_EMULATED_" + strings.ReplaceAll(spk.Name, " ", "") + "\r\n" +
				"\r\n"
			respConn.Write([]byte(response))
		}
		respConn.Close()
	}
}

// --------------- Per-Speaker HTTP Server ---------------

func startSpeakerHTTP(spk *VirtualSpeaker) {
	mux := http.NewServeMux()
	mux.HandleFunc("/xml/device_description.xml", func(w http.ResponseWriter, r *http.Request) {
		handleDeviceDescription(w, r, spk)
	})
	mux.HandleFunc("/MediaRenderer/AVTransport/Control", func(w http.ResponseWriter, r *http.Request) {
		handleSOAPAction(w, r, spk)
	})

	addr := fmt.Sprintf(":%d", spk.Port)
	log.Printf("[%s] HTTP server starting on %s", spk.Name, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[%s] HTTP server failed: %v", spk.Name, err)
	}
}

func handleDeviceDescription(w http.ResponseWriter, r *http.Request, spk *VirtualSpeaker) {
	log.Printf("[%s] Device description requested by %s", spk.Name, r.RemoteAddr)
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <roomName>%s</roomName>
    <displayName>%s</displayName>
    <modelName>Sonos One (Emulated)</modelName>
  </device>
</root>`, spk.Name, spk.Name)
}

func handleSOAPAction(w http.ResponseWriter, r *http.Request, spk *VirtualSpeaker) {
	soapAction := r.Header.Get("SOAPAction")
	body, _ := io.ReadAll(r.Body)
	bodyStr := string(body)

	// Extract action name from header
	// Format: "urn:schemas-upnp-org:service:AVTransport:1#SetAVTransportURI"
	action := soapAction
	if idx := strings.LastIndex(soapAction, "#"); idx >= 0 {
		action = soapAction[idx+1:]
	}
	action = strings.Trim(action, `"`)

	switch action {
	case "SetAVTransportURI":
		mediaURI := extractTagValue(bodyStr, "CurrentURI")
		spk.MediaURI = mediaURI
		log.Printf("[%s] SetAVTransportURI -> URI: %s", spk.Name, mediaURI)

	case "Play":
		log.Printf("[%s] Play (URI: %s)", spk.Name, spk.MediaURI)
		if *play && spk.MediaURI != "" {
			go playAudio(spk.Name, spk.MediaURI)
		} else if *verify && spk.MediaURI != "" {
			go verifyMediaURL(spk.Name, spk.MediaURI)
		}

	default:
		log.Printf("[%s] Unknown SOAP action: %s", spk.Name, action)
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:%sResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"/>
  </s:Body>
</s:Envelope>`, action)
}

// --------------- Helpers ---------------

func extractTagValue(body, tag string) string {
	start := strings.Index(body, "<"+tag+">")
	if start < 0 {
		return ""
	}
	start += len(tag) + 2
	end := strings.Index(body[start:], "</"+tag+">")
	if end < 0 {
		return ""
	}
	value := body[start : start+end]
	value = strings.ReplaceAll(value, "&amp;", "&")
	value = strings.ReplaceAll(value, "&lt;", "<")
	value = strings.ReplaceAll(value, "&gt;", ">")
	value = strings.ReplaceAll(value, "&quot;", `"`)
	return value
}

func playAudio(speakerName, mediaURL string) {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(mediaURL)
	if err != nil {
		log.Printf("[%s] PLAY FAILED - download error: %v", speakerName, err)
		return
	}
	defer resp.Body.Close()

	// Determine file extension from URL
	ext := filepath.Ext(mediaURL)
	if ext == "" {
		ext = ".m4a"
	}

	tmpFile, err := os.CreateTemp("", "sonos-emulator-*"+ext)
	if err != nil {
		log.Printf("[%s] PLAY FAILED - temp file error: %v", speakerName, err)
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		log.Printf("[%s] PLAY FAILED - write error: %v", speakerName, err)
		return
	}
	tmpFile.Close()

	log.Printf("[%s] Playing audio: %s", speakerName, mediaURL)
	cmd := exec.Command("afplay", tmpPath)
	if err := cmd.Run(); err != nil {
		log.Printf("[%s] PLAY FAILED - afplay error: %v", speakerName, err)
		return
	}
	log.Printf("[%s] Playback finished", speakerName)
}

func verifyMediaURL(speakerName, url string) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Head(url)
	if err != nil {
		log.Printf("[%s] VERIFY FAILED for %s: %v", speakerName, url, err)
		return
	}
	resp.Body.Close()
	log.Printf("[%s] VERIFY OK: %s -> %d (%s)", speakerName, url, resp.StatusCode, resp.Header.Get("Content-Type"))
}
