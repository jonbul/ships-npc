package main

import (
	"crypto/tls"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// npcClient owns the single websocket connection to ships-go and is
// responsible for (re)connecting, authenticating as an NPC controller,
// forwarding incoming gameBroadcast messages to the player tracker, and
// pushing outgoing npcUpdate batches.
type npcClient struct {
	url         string
	secret      string
	insecureTLS bool
	players     *playerTracker

	mu   sync.Mutex
	conn *websocket.Conn
}

func newNpcClient(url, secret string, insecureTLS bool, players *playerTracker) *npcClient {
	return &npcClient{url: url, secret: secret, insecureTLS: insecureTLS, players: players}
}

// run connects (and reconnects, with backoff, on failure) forever, reading
// incoming messages until the connection drops.
func (c *npcClient) run() {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if err := c.connectAndRead(); err != nil {
			log.Println("ships-npc: connection error:", err)
		}
		log.Printf("ships-npc: reconnecting in %s\n", backoff)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *npcClient) connectAndRead() error {
	dialer := websocket.DefaultDialer
	if c.insecureTLS {
		dialer = &websocket.Dialer{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- localhost-only, self-signed dev cert
		}
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	log.Println("ships-npc: connected to", c.url)

	if err := c.sendEnvelope(npcAuthMsg{EventName: "npcAuth", Secret: c.secret}); err != nil {
		return err
	}

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg gameBroadcast
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Println("ships-npc: invalid message from ships-go:", err)
			continue
		}
		if msg.EventName == "gameBroadcast" {
			c.players.apply(msg)
		}
	}
}

// sendUpdate pushes the full current NPC batch to ships-go in a single
// websocket frame, which is the most efficient way to keep an arbitrary
// number of NPCs in sync regardless of how many there are.
func (c *npcClient) sendUpdate(npcs []NpcData) error {
	return c.sendEnvelope(npcUpdateMsg{EventName: "npcUpdate", Npcs: npcs})
}

func (c *npcClient) sendEnvelope(event any) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.WriteJSON([]any{event})
}
