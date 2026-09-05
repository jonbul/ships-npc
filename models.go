package main

// PlayerData mirrors the subset of ships-go's PlayerData that ships-npc
// needs to make decisions (e.g. where to spawn a new NPC). Kept minimal on
// purpose: this service never needs credits, life, ship id, etc.
type PlayerData struct {
	SocketId string  `json:"socketId"`
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
}

// NpcData is the wire-compatible twin of ships-go's
// controllers/websocket/models.NpcData. Any NPC kind (black hole today,
// more in the future) is represented with this same generic shape,
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
}

// NpcTypes mirrors ships-go's NpcTypes enum-like value.
var NpcTypes = struct {
	BlackHole string
}{
	BlackHole: "BlackHole",
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
