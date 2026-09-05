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
	// EnemyShips is how many hostile Ship NPCs are simulated at once.
	// 0 disables them entirely; lowering it despawns the excess (silently,
	// with no kill-feed entry, since nobody killed them).
	EnemyShips int `json:"enemyShips"`
	// EnemyShipLife is a new ship's starting/maximum life. Existing ships
	// keep the value they spawned with.
	EnemyShipLife float32 `json:"enemyShipLife"`
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
}

// defaultNpcSettings are the values this service uses until ships-go tells
// it otherwise, and are duplicated as the defaults on the ships-go side so
// both agree before an admin has ever touched the panel.
func defaultNpcSettings() npcSettings {
	return npcSettings{
		EnemyShips:              1,
		EnemyShipLife:           10,
		EnemyShipSpeed:          defaultEnemyShipSpeed,
		EnemyShipFireRateMs:     500,
		MaxBlackHoles:           2,
		BlackHoleSpawnPeriodSec: 30,
	}
}

// sanitized clamps incoming settings into workable ranges, so a bad or
// partial payload can never wedge the simulation (a zero fire rate would
// fire every tick, a zero spawn period would spawn a black hole every
// tick, negative counts would underflow loops).
func (s npcSettings) sanitized() npcSettings {
	d := defaultNpcSettings()
	if s.EnemyShips < 0 {
		s.EnemyShips = 0
	}
	if s.EnemyShips > maxEnemyShips {
		s.EnemyShips = maxEnemyShips
	}
	if s.EnemyShipLife <= 0 {
		s.EnemyShipLife = d.EnemyShipLife
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
	return s
}

func (s npcSettings) fireCooldown() time.Duration {
	return time.Duration(s.EnemyShipFireRateMs) * time.Millisecond
}

func (s npcSettings) blackHoleSpawnPeriod() time.Duration {
	return time.Duration(s.BlackHoleSpawnPeriodSec) * time.Second
}

const (
	maxEnemyShips              = 20
	maxBlackHolesLimit         = 50
	minFireRateMs              = 100
	minBlackHoleSpawnPeriodSec = 1
)
