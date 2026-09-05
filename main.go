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
	secret := os.Getenv("NPC_SECRET")
	if secret == "" {
		log.Fatal("NPC_SECRET must be set (must match ships-go's NPC_SECRET)")
	}
	insecureTLS := envBool("NPC_TLS_INSECURE_SKIP_VERIFY", true)
	tickInterval := envDuration("NPC_TICK_INTERVAL_MS", 100*time.Millisecond)

	players := newPlayerTracker()
	simulator := newNpcSimulator()
	client := newNpcClient(wsURL, secret, insecureTLS, players)

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
