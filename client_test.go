package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// goNpcConfigFrame is the exact JSON ships-go emits for an npcConfig event
// (generated from models.NpcConfigData). Pinning the real payload here
// catches a rename on either side of the wire: the two services are
// separate Go modules with hand-mirrored structs, so nothing else would.
const goNpcConfigFrame = `{"eventName":"npcConfig","settings":{"enemyShipController":"both",` +
	`"enemyShips":3,"aiShips":4,"shipLife":35,"enemyShipSpeed":42,"enemyShipFireRateMs":800,` +
	`"maxBlackHoles":4,"blackHoleSpawnPeriodSec":15,"blackHoleDurationSec":90,` +
	`"contactDamage":true,"killScaling":true,` +
	`"shipSize":250,` +
	`"npcAttacksPlayers":true,"npcAttacksNpc":false,` +
	`"npcAttacksAi":true,"aiAttacksPlayers":true,"aiAttacksNpc":false,"aiAttacksAi":false}}`

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
		EnemyShipController: controllerBoth,
		EnemyShips:          3,
		AiShips:             4,
		ShipLife:            35, EnemyShipSpeed: 42,
		EnemyShipFireRateMs: 800, MaxBlackHoles: 4, BlackHoleSpawnPeriodSec: 15,
		BlackHoleDurationSec: 90,
		ContactDamage:        true,
		KillScaling:          true,
		ShipSize:             250,
		NpcAttacksPlayers:    true, NpcAttacksAi: true, AiAttacksPlayers: true,
	}
	if got != want {
		t.Fatalf("settings from ships-go not applied:\n got %+v\nwant %+v", got, want)
	}
	if wantSpeed := 42 * sim.frameScale; sim.maxSpeed != wantSpeed {
		t.Fatalf("speed envelope not recomputed: got %v want %v", sim.maxSpeed, wantSpeed)
	}
}

// TestConcurrentSendsAreSerialized reproduces the real production layout:
// the tick loop sends npcUpdate on the main goroutine while this client's
// read goroutine answers an incoming npcHit with removeBullet/playerDied.
// gorilla/websocket allows only one writer per connection, so without
// npcClient serializing them the two interleave into a corrupt frame (and
// the race detector flags it).
func TestConcurrentSendsAreSerialized(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := newNpcClient("ws"+strings.TrimPrefix(server.URL, "http"), "secret", false, newPlayerTracker())
	go client.run()

	deadline := time.Now().Add(5 * time.Second)
	for {
		client.mu.Lock()
		ready := client.conn != nil
		client.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("client never connected")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if g%2 == 0 {
					_ = client.sendUpdate([]NpcData{{Id: "npc", X: float64(i)}})
				} else {
					_ = client.sendRemoveBullet("bullet")
				}
			}
		}(g)
	}
	wg.Wait()
}
