package main

// PlayerData mirrors the subset of ships-go's PlayerData that ships-npc
// needs to make decisions (e.g. where to spawn a new NPC, who to target).
// Note X/Y are the ship's top-left anchor, not its center - ShipId is what
// lets us resolve its size and aim at the middle of it (see shipRegistry).
type PlayerData struct {
	SocketId string  `json:"socketId"`
	ShipId   string  `json:"shipId"`
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	IsDead   bool    `json:"isDead"`
}

// NpcData is the wire-compatible twin of ships-go's
// controllers/websocket/models.NpcData. Any NPC kind (black hole, enemy
// ship, more in the future) is represented with this same generic shape,
// distinguished by Type.
type NpcData struct {
	Type      string  `json:"type"`
	Id        string  `json:"id"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Scale     float64 `json:"scale"`
	MaxSize   int     `json:"maxSize"`
	Direction float64 `json:"direction"`
	Duration  int     `json:"duration"`
	Speed     float64 `json:"speed"`
	// Ship NPC fields (Type == NpcTypes.Ship).
	ShipId  string  `json:"shipId,omitempty"`
	Name    string  `json:"name,omitempty"`
	Rotate  float32 `json:"rotate,omitempty"`
	Life    float32 `json:"life,omitempty"`
	MaxLife float32 `json:"maxLife,omitempty"`
}

// NpcTypes mirrors ships-go's NpcTypes enum-like value.
var NpcTypes = struct {
	BlackHole string
	Ship      string
}{
	BlackHole: "BlackHole",
	Ship:      "Ship",
}

// npcAuthMsg is sent once, right after connecting, to identify this
// connection to ships-go as a trusted NPC controller rather than a player.
type npcAuthMsg struct {
	EventName string `json:"eventName"`
	Secret    string `json:"secret"`
}

// npcUpdateMsg carries the full, current set of NPCs in a single batch.
// Sending everything at once (instead of one message per NPC) is the most
// efficient option for gorilla/websocket: one write, one frame, regardless
// of how many NPCs are being simulated.
type npcUpdateMsg struct {
	EventName string    `json:"eventName"`
	Npcs      []NpcData `json:"npcs"`
}

// gameBroadcast is the (partial) shape of the message ships-go sends to
// every connected socket, including this NPC controller. Only the fields
// ships-npc actually needs are declared.
type gameBroadcast struct {
	EventName       string                `json:"eventName"`
	Players         map[string]PlayerData `json:"players"`
	ActivePlayerIds []string              `json:"activePlayerIds"`
}

// npcConfigMsg carries the NPC settings an administrator chose in
// ships-vue's admin panel. ships-go owns them (it's the only service the
// browser talks to) and pushes them down this connection whenever they
// change and right after npcAuth succeeds, so this service converges on
// the current values without needing its own HTTP endpoint or restart.
type npcConfigMsg struct {
	EventName string      `json:"eventName"`
	Settings  npcSettings `json:"settings"`
}

// npcHitMsg is forwarded by ships-go when a player's bullet hits one of our
// Ship NPCs (see ships-vue's checkBulletCollision). ships-go doesn't track
// NPC health, so this service is the one deciding what happens next.
type npcHitMsg struct {
	EventName    string  `json:"eventName"`
	NpcId        string  `json:"npcId"`
	BulletId     string  `json:"bulletId"`
	From         string  `json:"from"`
	BulletCharge float32 `json:"bulletCharge"`
	X            float32 `json:"x"`
	Y            float32 `json:"y"`
}

// newBulletMsg is wire-compatible with ships-go's BulletData. Sending one
// lets every connected client render/animate our Ship NPC's shot exactly
// like a player-fired bullet, with no ships-vue changes needed for that
// part.
type newBulletMsg struct {
	EventName     string  `json:"eventName"`
	SocketId      string  `json:"socketId"`
	Angle         float32 `json:"angle"`
	BulletCharge  float32 `json:"bulletCharge"`
	ExpX          float32 `json:"expX"`
	ExpY          float32 `json:"expY"`
	Id            string  `json:"id"`
	MoveX         float32 `json:"moveX"`
	MoveY         float32 `json:"moveY"`
	Rotation      float32 `json:"rotation"`
	ShootingSpeed float32 `json:"shootingSpeed"`
	X             float32 `json:"x"`
	Y             float32 `json:"y"`
}

// removeBulletMsg tells every client to stop rendering a bullet. ships-go's
// handler only reads BulletId, so this minimal shape is enough.
type removeBulletMsg struct {
	EventName string `json:"eventName"`
	BulletId  string `json:"bulletId"`
}

// playerDiedMsg reuses ships-go's existing playerDied handling (credits the
// killer, adds a kill-feed entry) to announce a Ship NPC's death: PlayerId
// is the NPC's id, From is the killer's real socketId. ships-go doesn't
// need to know the "player" that died was actually an NPC.
type playerDiedMsg struct {
	EventName string  `json:"eventName"`
	BulletId  string  `json:"bulletId"`
	From      string  `json:"from"`
	PlayerId  string  `json:"playerId"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
}

// publicShip is the subset of ships-go's GET /game/getShips response this
// service needs: a random ship id/name for a new enemy Ship NPC, plus its
// size so bullets can be spawned from its actual center (mirrors
// ships-vue's Player.getCenteredPosition(), which uses the ship's raw,
// unscaled width/height - see CHANGES.md).
type publicShip struct {
	Id     string `json:"_id"`
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Canvas struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"canvas"`
}

// centerOffset returns the ship's half-width/half-height, falling back to
// its canvas size like ships-vue's Player constructor does
// (this.width = this.ship.width || this.ship.canvas.width).
//
// This is deliberately based on the ship's raw, unscaled size: ships-vue's
// visual scale (kills/deaths based) shifts x/y by xTranslation/yTranslation
// = (width - realWidth)/2, so the true center is always x + width/2
// regardless of scale - exactly what Player.getCenteredPosition() returns.
func (s publicShip) centerOffset() (float64, float64) {
	width := s.Width
	if width == 0 {
		width = s.Canvas.Width
	}
	height := s.Height
	if height == 0 {
		height = s.Canvas.Height
	}
	return float64(width) / 2, float64(height) / 2
}
