package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// goNpcConfigFrame is the exact JSON ships-go emits for an npcConfig event
// (generated from models.NpcConfigData). Pinning the real payload here
// catches a rename on either side of the wire: the two services are
// separate Go modules with hand-mirrored structs, so nothing else would.
const goNpcConfigFrame = `{"eventName":"npcConfig","settings":{"enemyShips":3,"enemyShipLife":35,` +
	`"enemyShipSpeed":42,"enemyShipFireRateMs":800,"maxBlackHoles":4,"blackHoleSpawnPeriodSec":15}}`

// TestAppliesNpcConfigFromShipsGo runs the whole path an admin's save takes
// on this side: a websocket frame from ships-go, decoded by npcClient and
// handed to the simulator, which must be running with the new settings.
func TestAppliesNpcConfigFromShipsGo(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Wait for npcAuth, then push the settings like ships-go does.
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(goNpcConfigFrame))
		// Hold the connection open so the client doesn't reconnect-loop.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	f := &fakeSender{}
	sim := settingsSim(f, defaultNpcSettings())

	client := newNpcClient("ws"+strings.TrimPrefix(server.URL, "http"), "secret", false, newPlayerTracker())
	applied := make(chan struct{})
	client.onSettings = func(settings npcSettings) {
		sim.applySettings(settings)
		close(applied)
	}
	go client.run()

	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for npcConfig to be applied")
	}

	got := sim.currentSettings()
	want := npcSettings{
		EnemyShips: 3, EnemyShipLife: 35, EnemyShipSpeed: 42,
		EnemyShipFireRateMs: 800, MaxBlackHoles: 4, BlackHoleSpawnPeriodSec: 15,
	}
	if got != want {
		t.Fatalf("settings from ships-go not applied:\n got %+v\nwant %+v", got, want)
	}
	if wantSpeed := 42 * sim.frameScale; sim.maxSpeed != wantSpeed {
		t.Fatalf("speed envelope not recomputed: got %v want %v", sim.maxSpeed, wantSpeed)
	}
}
