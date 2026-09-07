package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
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
	// onKills is called with the kill events ships-go relays, so an NPC can
	// be credited for the players it shot down. The hit itself is always
	// detected by the victim's client, never here.
	onKills func([]killEventData)
	// onBullets is called with every shot ships-go broadcasts, so the
	// simulator can see incoming fire and fly out of its way.
	onBullets func([]newBulletMsg)

	mu   sync.Mutex
	conn *websocket.Conn

	// writeMu serializes writes. gorilla/websocket supports exactly one
	// concurrent writer per connection, and sends genuinely come from two
	// goroutines: the tick loop (npcUpdate, bullets, expiries) and this
	// client's own read goroutine, where an incoming npcHit is answered
	// with removeBullet/playerDied. The simulator's lock does not cover
	// this - sendUpdate is called after tick() has already released it.
	writeMu sync.Mutex
}

// writeTimeout bounds a single frame write. Sends are made while the
// simulator holds its lock, so a write that blocked forever would freeze
// every NPC with it rather than just delaying one message.
const writeTimeout = 10 * time.Second

func newNpcClient(url, secret string, insecureTLS bool, players *playerTracker) *npcClient {
	return &npcClient{url: url, secret: secret, insecureTLS: insecureTLS, players: players}
}

// run connects (and reconnects, with backoff, on failure) forever, reading
// incoming messages until the connection drops.
func (c *npcClient) run() {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		connectedAt := time.Now()
		if err := c.connectAndRead(); err != nil {
			log.Println("ships-npc: connection error:", err)
		}
		// A connection that lasted a while was healthy, so don't carry the
		// previous failure's backoff into it: otherwise a single early
		// hiccup leaves ships-go without NPCs for 30s after every drop.
		if time.Since(connectedAt) > maxBackoff {
			backoff = time.Second
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
			if c.onKills != nil {
				c.onKills(msg.Kills)
			}
			if c.onBullets != nil && len(msg.NewBullets) > 0 {
				c.onBullets(msg.NewBullets)
			}
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
		case "npcRejected":
			// ships-go only lets one controller drive the NPCs, because two
			// of them overwrite each other's snapshot every tick and make
			// the NPCs flicker for players. Almost always this means a
			// previous ships-npc is still running.
			var msg struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(raw, &msg)
			return fmt.Errorf("ships-go refused this NPC controller: %s "+
				"(is another ships-npc still running?)", msg.Reason)
		}
	}
}

// sendUpdate pushes the full current NPC batch to ships-go in a single
// websocket frame, which is the most efficient way to keep an arbitrary
// number of NPCs in sync regardless of how many there are.
func (c *npcClient) sendUpdate(npcs []NpcData) error {
	return c.sendEnvelope(npcUpdateMsg{EventName: "npcUpdate", Npcs: npcs})
}

// sendMetrics reports this process's resource usage so ships-go can put it
// on /metrics. Sent on its own slow ticker rather than with the per-tick
// npcUpdate: this changes on the scale of seconds, and a Grafana dashboard
// does not need it ten times a second.
func (c *npcClient) sendMetrics(m processMetrics) error {
	return c.sendEnvelope(npcMetricsMsg{
		EventName:     "npcMetrics",
		CpuSeconds:    m.CPUSeconds,
		CpuPercent:    m.CPUPercent,
		ResidentBytes: m.ResidentBytes,
		HeapBytes:     m.HeapBytes,
		HeapSysBytes:  m.HeapSysBytes,
		Goroutines:    m.Goroutines,
		Npcs:          m.Npcs,
		TickSeconds:   m.TickSeconds,
	})
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

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := conn.WriteJSON([]any{event})
	if err != nil {
		// Any write error on a websocket leaves the connection unusable,
		// and a timeout leaves it permanently broken for writes while the
		// read side happily blocks on. Closing it makes the reader return
		// so run() reconnects instead of running blind.
		_ = conn.Close()
	}
	return err
}
