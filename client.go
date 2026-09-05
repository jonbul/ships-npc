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
	// onNpcHit is called whenever ships-go forwards an npcHit event (a
	// player's bullet hit one of our Ship NPCs). Set before run().
	onNpcHit func(npcHitMsg)
	// onSettings is called whenever ships-go pushes an npcConfig event: the
	// admin changed the NPC settings, or this connection just authenticated
	// and is being told the values currently in force. Set before run().
	onSettings func(npcSettings)

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
		var meta struct {
			EventName string `json:"eventName"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			log.Println("ships-npc: invalid message from ships-go:", err)
			continue
		}
		switch meta.EventName {
		case "gameBroadcast":
			var msg gameBroadcast
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Println("ships-npc: invalid gameBroadcast:", err)
				continue
			}
			c.players.apply(msg)
		case "npcConfig":
			var cfg npcConfigMsg
			if err := json.Unmarshal(raw, &cfg); err != nil {
				log.Println("ships-npc: invalid npcConfig:", err)
				continue
			}
			if c.onSettings != nil {
				c.onSettings(cfg.Settings)
			}
		case "npcHit":
			var hit npcHitMsg
			if err := json.Unmarshal(raw, &hit); err != nil {
				log.Println("ships-npc: invalid npcHit:", err)
				continue
			}
			if c.onNpcHit != nil {
				c.onNpcHit(hit)
			}
		}
	}
}

// sendUpdate pushes the full current NPC batch to ships-go in a single
// websocket frame, which is the most efficient way to keep an arbitrary
// number of NPCs in sync regardless of how many there are.
func (c *npcClient) sendUpdate(npcs []NpcData) error {
	return c.sendEnvelope(npcUpdateMsg{EventName: "npcUpdate", Npcs: npcs})
}

// sendBullet broadcasts a new Ship NPC-fired bullet to every client, using
// the exact same event ships-go/ships-vue already use for player-fired
// bullets - no frontend changes needed to render/animate it.
func (c *npcClient) sendBullet(b newBulletMsg) error {
	b.EventName = "newBullet"
	return c.sendEnvelope(b)
}

// sendRemoveBullet tells every client to stop rendering a bullet this
// service fired, once it's traveled its full range.
func (c *npcClient) sendRemoveBullet(bulletId string) error {
	return c.sendEnvelope(removeBulletMsg{EventName: "removeBullet", BulletId: bulletId})
}

// sendPlayerDied reuses ships-go's existing playerDied handling to credit
// the killer and add a kill-feed entry when a Ship NPC is destroyed.
func (c *npcClient) sendPlayerDied(msg playerDiedMsg) error {
	msg.EventName = "playerDied"
	return c.sendEnvelope(msg)
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
