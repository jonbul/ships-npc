package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiBaseURLFromWsURL derives ships-go's HTTP(S) base URL from its
// websocket URL (e.g. wss://localhost:3000/ws -> https://localhost:3000),
// so a single NPC_WS_URL env var is enough to reach both endpoints. Use
// NPC_API_URL to override this if ships-go's REST API lives elsewhere.
func apiBaseURLFromWsURL(wsURL string) string {
	url := wsURL
	url = strings.Replace(url, "wss://", "https://", 1)
	url = strings.Replace(url, "ws://", "http://", 1)
	url = strings.TrimSuffix(url, "/ws")
	return url
}

// fetchPublicShips calls ships-go's GET /game/getShips (the same endpoint
// the game client uses to populate its ship picker) so this service can
// spawn enemy NPCs using any of the same public ships players can use.
func fetchPublicShips(apiBaseURL string, insecureTLS bool) ([]publicShip, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	if insecureTLS {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- localhost-only, self-signed dev cert
		}
	}

	resp, err := client.Get(apiBaseURL + "/game/getShips")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /game/getShips: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var ships []publicShip
	if err := json.NewDecoder(resp.Body).Decode(&ships); err != nil {
		return nil, err
	}
	return ships, nil
}
