package main

import (
	"log"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	wsURL := envOr("NPC_WS_URL", "wss://localhost:3000/ws")
	apiURL := envOr("NPC_API_URL", apiBaseURLFromWsURL(wsURL))
	secret := os.Getenv("NPC_SECRET")
	if secret == "" {
		log.Fatal("NPC_SECRET must be set (must match ships-go's NPC_SECRET)")
	}
	insecureTLS := envBool("NPC_TLS_INSECURE_SKIP_VERIFY", true)
	tickInterval := envDuration("NPC_TICK_INTERVAL_MS", 100*time.Millisecond)

	// Ships available for the enemy Ship NPC to use, fetched from the same
	// endpoint the game client uses to populate its own ship picker
	// (GET /game/getShips). Retried a few times in case ships-go isn't up
	// yet; if it never succeeds, ships-npc still runs fine (black holes
	// only, no enemy ship until ships are fetched successfully).
	ships, err := fetchPublicShipsWithRetry(apiURL, insecureTLS, 5, 2*time.Second)
	if err != nil {
		log.Println("ships-npc: could not fetch public ships, enemy ship NPC disabled for now:", err)
	} else {
		log.Printf("ships-npc: loaded %d public ships for the enemy ship NPC\n", len(ships))
	}

	players := newPlayerTracker()
	client := newNpcClient(wsURL, secret, insecureTLS, players)
	simulator := newNpcSimulator(client, ships, tickInterval)
	client.onNpcHit = simulator.handleNpcHit
	client.onSettings = func(settings npcSettings) {
		simulator.applySettings(settings)
		log.Printf("ships-npc: applied NPC settings from admin: %+v\n", simulator.currentSettings())
	}

	go client.run()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for range ticker.C {
		if players.count() == 0 {
			continue
		}
		simulator.tick(players.snapshot())
		if err := client.sendUpdate(simulator.snapshot()); err != nil {
			log.Println("ships-npc: failed to send npcUpdate:", err)
		}
	}
}

// fetchPublicShipsWithRetry retries fetchPublicShips a few times with a
// fixed delay, since ships-npc may start up before ships-go is ready.
func fetchPublicShipsWithRetry(apiURL string, insecureTLS bool, attempts int, delay time.Duration) ([]publicShip, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var ships []publicShip
		ships, err = fetchPublicShips(apiURL, insecureTLS)
		if err == nil {
			return ships, nil
		}
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return nil, err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	ms, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}
