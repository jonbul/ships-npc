package main

import "time"

// npcSettings is the set of NPC knobs an administrator can change at
// runtime from ships-vue's admin panel. ships-go owns the current values
// (it's the only service the admin page can talk to) and pushes them down
// this service's existing websocket as an `npcConfig` event, both when
// they change and right after this service authenticates - so a restart of
// either process converges on the same settings without an extra endpoint,
// port or credential.
//
// Everything here is applied live, on the next tick: no respawn or
// reconnect needed.
type npcSettings struct {
	// EnemyShipController chooses which brains fly the hostile ships:
	// "none", "rule" (the hand-written controller), "ai" (the learned
	// policy in ai.go) or "both" - which runs the two fleets side by side,
	// on the same physics and the same tunables below, so they can be
	// compared honestly. Each fleet keeps its own size, so switching away
	// and back does not lose the number you had set.
	EnemyShipController string `json:"enemyShipController"`
	// EnemyShips is how many rule-flown hostile Ship NPCs are simulated at
	// once. 0 disables them entirely; lowering it despawns the excess
	// (silently, with no kill-feed entry, since nobody killed them).
	EnemyShips int `json:"enemyShips"`
	// AiShips is the same, for the ships flown by the learned policy.
	AiShips int `json:"aiShips"`
	// ShipLife is a new ship's starting/maximum life, and applies to
	// players too (ships-go relays it to the browsers). Existing ships keep
	// the value they spawned with.
	ShipLife float32 `json:"shipLife"`
	// EnemyShipSpeed is a ship's cruising speed in the game's own speed
	// units - the same scale as ships-vue's SPEED.MAX (clientSpeedMax), so
	// clientSpeedMax means "as fast as a player at full throttle" and can
	// never be outrun. Escaping a black hole always uses the full envelope
	// regardless of this, since being sucked in is fatal.
	EnemyShipSpeed float64 `json:"enemyShipSpeed"`
	// EnemyShipFireRateMs is the cooldown between a ship's shots.
	EnemyShipFireRateMs int `json:"enemyShipFireRateMs"`
	// MaxBlackHoles caps how many black holes exist at once; 0 disables
	// new ones (the existing ones still live out their duration).
	MaxBlackHoles int `json:"maxBlackHoles"`
	// BlackHoleSpawnPeriodSec is the delay between black hole spawns.
	BlackHoleSpawnPeriodSec int `json:"blackHoleSpawnPeriodSec"`
	// BlackHoleDurationSec is how long a black hole lives before it starts
	// shrinking away. It is read every tick rather than stamped on a black
	// hole at spawn, so shortening it also retires the ones already on the
	// map instead of only applying to the next one.
	BlackHoleDurationSec int `json:"blackHoleDurationSec"`
	// ContactDamage turns ramming damage on or off for *everyone* - players
	// and both NPC fleets alike. It is not an NPC-only setting: ships-go
	// also broadcasts it to every browser, because player-vs-player contact
	// is resolved there. Ships still push each other apart when it is off;
	// only the damage stops.
	ContactDamage bool `json:"contactDamage"`
	// KillScaling makes a player's ship grow with its score. Nothing here
	// reads it - it is a rule browsers enforce for themselves, and NPC ships
	// have a fixed scale - but it is mirrored so this struct stays field for
	// field identical to ships-go's NpcSettingsData. That identity is what
	// lets one test pin the whole wire contract; a "superset on one side"
	// arrangement would quietly let real fields go missing.
	KillScaling bool `json:"killScaling"`
	// ShipSize is the size every ship is normalised to, whatever its
	// artwork measures: ships-vue divides this by the ship's largest raw
	// dimension to get the scale it draws (and collides) at. This service
	// has to apply the same rule, or a ship built on a 400px canvas would
	// be flown as if it were four times the size a player can see.
	ShipSize int `json:"shipSize"`
	// The attack matrix: for each kind of attacker, which factions it
	// treats as valid targets. Any combination is allowed, including none
	// (a pacifist fleet that still flies and dodges but never shoots) and
	// self-targeting, which makes a fleet fight among itself.
	//
	// A ship hunts the nearest thing it is allowed to attack, and only
	// damages what it is allowed to attack - so with Npc->Ai on but
	// Ai->Npc off, the AI ships are hunted and never shoot back.
	//
	// Player-vs-NPC damage is reported by the shooting player's own client
	// (an npcHit event), because only that client knows where its bullet
	// is. Nobody's client tracks NPC-vs-NPC, so this service resolves those
	// hits itself - which is why it has to keep flying its own bullets (see
	// activeBullets) instead of only timing them out.
	//
	// None of these are `omitempty`: false is meaningful, and an admin
	// clearing a box must actually clear it downstream rather than have the
	// field vanish and leave this service on its previous value.
	NpcAttacksPlayers bool `json:"npcAttacksPlayers"`
	NpcAttacksNpc     bool `json:"npcAttacksNpc"`
	NpcAttacksAi      bool `json:"npcAttacksAi"`
	AiAttacksPlayers  bool `json:"aiAttacksPlayers"`
	AiAttacksNpc      bool `json:"aiAttacksNpc"`
	AiAttacksAi       bool `json:"aiAttacksAi"`
}

// controllerKind is which brain flies a ship. It is also the ship's
// faction, since who may shoot whom is decided per brain.
type controllerKind string

const (
	controllerRule controllerKind = "rule"
	controllerAi   controllerKind = "ai"
)

// namePrefix tags a ship's name with the brain flying it, so a player can
// tell at a glance which fleet is which - on the ship itself and in the
// scoreboard, where the two fleets' scores are the whole point of running
// them together.
func (c controllerKind) namePrefix() string {
	if c == controllerAi {
		return "[AI]"
	}
	return "[NPC]"
}

// faction is a target's allegiance: the two ship controllers, plus the
// players.
type faction string

const factionPlayers faction = "players"

func (c controllerKind) faction() faction { return faction(c) }

// enabled reports whether the controller should have a fleet at all,
// which is the "none/rule/ai/both" choice collapsed to a single question.
func (s npcSettings) enabled(c controllerKind) bool {
	switch s.EnemyShipController {
	case controllerBoth:
		return true
	case string(controllerRule):
		return c == controllerRule
	case string(controllerAi):
		return c == controllerAi
	default: // controllerNone, or anything unrecognised
		return false
	}
}

// fleetSize is how many ships the given controller should be flying right
// now: its configured count, or none when it is switched off.
func (s npcSettings) fleetSize(c controllerKind) int {
	if !s.enabled(c) {
		return 0
	}
	if c == controllerAi {
		return s.AiShips
	}
	return s.EnemyShips
}

// attacks reports whether ships flown by attacker may hunt and damage
// target. A disabled controller attacks nothing, so bullets already in
// flight when a fleet is switched off stop counting against anyone.
func (s npcSettings) attacks(attacker controllerKind, target faction) bool {
	if !s.enabled(attacker) {
		return false
	}
	if target != factionPlayers && !s.enabled(controllerKind(target)) {
		return false
	}
	switch {
	case attacker == controllerRule && target == factionPlayers:
		return s.NpcAttacksPlayers
	case attacker == controllerRule && target == controllerRule.faction():
		return s.NpcAttacksNpc
	case attacker == controllerRule && target == controllerAi.faction():
		return s.NpcAttacksAi
	case attacker == controllerAi && target == factionPlayers:
		return s.AiAttacksPlayers
	case attacker == controllerAi && target == controllerRule.faction():
		return s.AiAttacksNpc
	case attacker == controllerAi && target == controllerAi.faction():
		return s.AiAttacksAi
	}
	return false
}

// defaultNpcSettings are the values this service uses until ships-go tells
// it otherwise, and are duplicated as the defaults on the ships-go side so
// both agree before an admin has ever touched the panel.
func defaultNpcSettings() npcSettings {
	return npcSettings{
		EnemyShipController:     string(controllerRule),
		EnemyShips:              1,
		AiShips:                 1,
		ShipLife:                defaultShipLife,
		EnemyShipSpeed:          defaultEnemyShipSpeed,
		EnemyShipFireRateMs:     500,
		MaxBlackHoles:           2,
		BlackHoleSpawnPeriodSec: 30,
		BlackHoleDurationSec:    defaultBlackHoleDurationSec,
		ContactDamage:           true,
		KillScaling:             false,
		ShipSize:                defaultShipSize,
		// Both fleets hunt players and nothing else by default; mirrors
		// the ships-go side.
		NpcAttacksPlayers: true,
		AiAttacksPlayers:  true,
	}
}

// sanitized clamps incoming settings into workable ranges, so a bad or
// partial payload can never wedge the simulation (a zero fire rate would
// fire every tick, a zero spawn period would spawn a black hole every
// tick, negative counts would underflow loops).
func (s npcSettings) sanitized() npcSettings {
	d := defaultNpcSettings()
	switch s.EnemyShipController {
	case controllerNone, string(controllerRule), string(controllerAi), controllerBoth:
	default:
		// An unrecognised value would otherwise silently mean "none",
		// making the whole fleet vanish because of a typo upstream.
		s.EnemyShipController = d.EnemyShipController
	}
	s.EnemyShips = clampCount(s.EnemyShips)
	s.AiShips = clampCount(s.AiShips)
	if s.ShipLife <= 0 {
		s.ShipLife = d.ShipLife
	}
	if s.EnemyShipSpeed <= 0 {
		s.EnemyShipSpeed = d.EnemyShipSpeed
	}
	if s.EnemyShipSpeed > clientSpeedMax {
		s.EnemyShipSpeed = clientSpeedMax
	}
	if s.EnemyShipFireRateMs < minFireRateMs {
		s.EnemyShipFireRateMs = minFireRateMs
	}
	if s.MaxBlackHoles < 0 {
		s.MaxBlackHoles = 0
	}
	if s.MaxBlackHoles > maxBlackHolesLimit {
		s.MaxBlackHoles = maxBlackHolesLimit
	}
	if s.BlackHoleSpawnPeriodSec < minBlackHoleSpawnPeriodSec {
		s.BlackHoleSpawnPeriodSec = minBlackHoleSpawnPeriodSec
	}
	// Zero means "not set" (an older ships-go), which has to mean the
	// default rather than the minimum.
	if s.BlackHoleDurationSec <= 0 {
		s.BlackHoleDurationSec = d.BlackHoleDurationSec
	}
	if s.BlackHoleDurationSec < minBlackHoleDurationSec {
		s.BlackHoleDurationSec = minBlackHoleDurationSec
	}
	if s.BlackHoleDurationSec > maxBlackHoleDurationSec {
		s.BlackHoleDurationSec = maxBlackHoleDurationSec
	}
	// Zero means "not set" - an older ships-go, or a payload predating the
	// field - and must fall back to the default rather than clamp up to
	// the minimum, which would shrink the whole fleet.
	if s.ShipSize <= 0 {
		s.ShipSize = d.ShipSize
	}
	if s.ShipSize < minShipSize {
		s.ShipSize = minShipSize
	}
	if s.ShipSize > maxShipSize {
		s.ShipSize = maxShipSize
	}
	return s
}

func clampCount(count int) int {
	if count < 0 {
		return 0
	}
	if count > maxEnemyShips {
		return maxEnemyShips
	}
	return count
}

func (s npcSettings) fireCooldown() time.Duration {
	return time.Duration(s.EnemyShipFireRateMs) * time.Millisecond
}

func (s npcSettings) blackHoleSpawnPeriod() time.Duration {
	return time.Duration(s.BlackHoleSpawnPeriodSec) * time.Second
}

func (s npcSettings) blackHoleDuration() time.Duration {
	return time.Duration(s.BlackHoleDurationSec) * time.Second
}

const (
	// controllerNone/controllerBoth are settings values rather than
	// controllers, so they are plain strings and not controllerKind.
	controllerNone = "none"
	controllerBoth = "both"

	// maxEnemyShips caps *each* fleet, so "both" at the maximum is twice
	// this many ships.
	maxEnemyShips      = 100
	maxBlackHolesLimit = 50
	// The standard ship size and life, mirroring ships-go.
	defaultShipLife            = 10
	defaultShipSize            = 100
	minShipSize                = 20
	maxShipSize                = 1000
	minFireRateMs              = 100
	minBlackHoleSpawnPeriodSec = 1
	// How long a black hole lives, and its bounds. The default is what was
	// hardcoded before it became a setting.
	defaultBlackHoleDurationSec = 180
	minBlackHoleDurationSec     = 5
	maxBlackHoleDurationSec     = 86400
)

// allControllers is every brain that can fly a ship, in a fixed order so
// the two fleets are always managed in the same sequence whatever the
// settings say.
var allControllers = []controllerKind{controllerRule, controllerAi}
