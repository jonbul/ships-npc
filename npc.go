package main

import (
	"log"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	blackHoleDuration    = 180 * time.Second
	blackHoleSpawnMargin = 2000
	blackHoleScaleStep   = 0.01
	blackHoleSpeed       = 7.5

	// Black hole gravity, mirrored 1:1 from ships-vue's applyNpcImpacts()
	// so an NPC ship is sucked in exactly like a player is. ships-vue
	// applies this once per fixed timestep, so the values are per client
	// frame and get rescaled to this service's tick rate (see frameScale).
	blackHolePullRange  = 2000.0 // edge distance at which gravity starts
	blackHolePullFactor = 1000.0
	blackHolePullBoost  = 1.25
	clientTickInterval  = time.Second / 30 // ships-vue's fixed timestep

	// Enemy ship: hostile Ship NPCs that chase the nearest player and
	// shoot at them. Any number can be simulated at once; how many is one
	// of the settings an admin can change live (see npcSettings).
	//
	// Its speed model is derived from ships-vue's SPEED constants
	// (public/js/utils/constants.js) rather than being invented here, so
	// the ship is actually comparable to a player: hardcoded values were
	// ~6x too slow, leaving it trailing forever at a near-constant bearing
	// and never reaching firing range. These are per *client frame*, so
	// they're rescaled to this service's tick rate (see frameScale).
	clientSpeedStep = 0.2  // SPEED.STEP
	clientSpeedMax  = 50.0 // SPEED.MAX

	// Cruising speed of an enemy ship, expressed in the game's own speed
	// units (the same scale as ships-vue's SPEED.MAX, i.e. px per client
	// frame) so an admin tunes it against a number that means something in
	// game terms rather than an abstract multiplier. Admin-tunable, so this
	// is only the default: at 20 against SPEED.MAX of 50, a ship cruises at
	// 40% of a player's top speed, so it's a real threat that a player at
	// full throttle can still outrun.
	defaultEnemyShipSpeed = 20.0
	enemyShipBrakeFactor  = 3.0 // brakes harder than it accelerates
	// Escaping a black hole is a full-throttle emergency, so it gets a
	// player's full top speed and a much punchier acceleration.
	enemyShipEscapeSpeedFactor = 1.0
	enemyShipEscapeAccelFactor = 3.0

	enemyShipStandoff = 500.0 // stop closing in once this close, to keep shooting range
	// Edge distance to a black hole at which the ship gives up on its
	// target and runs for its life. Sits just inside blackHolePullRange so
	// it turns and runs while the pull is still weak enough to out-run.
	// It then keeps running until it's fully clear of the hole's gravity
	// (Safe > blackHolePullRange): without that hysteresis a ship chasing
	// a player who's sitting on a black hole just oscillates across the
	// threshold, never escaping and never attacking.
	enemyShipBlackHoleFear = 1800.0
	enemyShipBlackHoleSafe = 2500.0
	enemyShipShootRange    = 1400.0
	enemyShipRespawnDelay  = 8 * time.Second
	enemyShipSpawnMargin   = 1500
	enemyShipScale         = 1.0
	npcBulletShootingSpeed = 0 // matches ships-vue's default (no charge bonus)
	npcBulletSpeed         = 25*1.5 + npcBulletShootingSpeed
	npcBulletRange         = 5000
	npcBulletCharge        = 3 // damage per hit; player life defaults to 10
	npcBulletLifetime      = 3 * time.Second
)

// npcSimulator owns and simulates every NPC (black holes, the enemy ship,
// more kinds in the future). It is the single source of truth for NPC
// state: ships-go no longer simulates anything, it only relays whatever
// this service sends.
type npcSimulator struct {
	mu        sync.Mutex
	npcs      map[string]*npcState
	lastSpawn time.Time

	// Enemy ships (destructible, chase + shoot the nearest player).
	sender         bulletSender
	ships          []publicShip
	shipsById      map[string]publicShip // to resolve a target player's ship size
	enemyShips     map[string]*enemyShipState
	enemyRespawnAt time.Time
	activeBullets  map[string]time.Time // bulletId -> firedAt

	// Live admin-tunable settings, replaced wholesale by applySettings
	// whenever ships-go pushes an npcConfig event. Read under mu like
	// everything else here, since the push arrives on the client's read
	// goroutine, not the tick loop.
	settings npcSettings

	// How many ships-vue frames one tick of this service represents. Both
	// the black hole pull and the ship's speed model are expressed in the
	// client's per-frame units, so they're rescaled by this to stay
	// equivalent in wall-clock terms whatever NPC_TICK_INTERVAL_MS is.
	frameScale float64

	// Enemy ship speed envelope, in px per tick, derived from frameScale.
	// maxSpeed also depends on the admin-tunable speed factor, so it's
	// recomputed by applySettings rather than fixed at construction.
	maxSpeed       float64
	accel          float64
	decel          float64
	escapeMaxSpeed float64
	escapeAccel    float64
}

type npcState struct {
	NpcData
	spawnedAt time.Time
}

// enemyShipState is the enemy ship's own npcState, kept separate from
// npcState (black holes) since it carries extra fields (life, target).
type enemyShipState struct {
	NpcData
	ship       publicShip // needed to fire bullets from the ship's actual center
	spawnedAt  time.Time
	fleeing    bool      // escaping a black hole; see enemyShipBlackHoleSafe
	lastShotAt time.Time // per ship, so several of them don't fire in lockstep
}

// radius is the ship's collision radius, resolved exactly like ships-vue
// does for any drawable it tests against a black hole: the half-diagonal
// of its bounding box (getRadiusFromRect).
func (e *enemyShipState) radius() float64 {
	offsetX, offsetY := e.ship.centerOffset()
	return math.Hypot(offsetX*e.Scale, offsetY*e.Scale)
}

// bulletSender is the subset of npcClient the simulator needs to fire
// bullets and announce kills, without depending on npcClient directly
// (keeps npc.go testable/independent of the transport).
type bulletSender interface {
	sendBullet(newBulletMsg) error
	sendRemoveBullet(bulletId string) error
	sendPlayerDied(playerDiedMsg) error
}

func newNpcSimulator(sender bulletSender, ships []publicShip, tickInterval time.Duration) *npcSimulator {
	// ships-vue resolves every player's ship from this same public list
	// (ShipsManager.getShipById, fed by GET /game/getShips), so indexing it
	// here is enough to know any player's ship dimensions.
	shipsById := make(map[string]publicShip, len(ships))
	for _, ship := range ships {
		shipsById[ship.Id] = ship
	}
	frameScale := float64(tickInterval) / float64(clientTickInterval)
	if frameScale <= 0 {
		frameScale = 1
	}
	// A speed in px/frame becomes px/tick by multiplying by frameScale; an
	// acceleration is per frame *per frame*, so it scales quadratically.
	accel := clientSpeedStep * frameScale * frameScale
	sim := &npcSimulator{
		npcs:           make(map[string]*npcState),
		sender:         sender,
		ships:          ships,
		shipsById:      shipsById,
		enemyShips:     make(map[string]*enemyShipState),
		activeBullets:  make(map[string]time.Time),
		frameScale:     frameScale,
		accel:          accel,
		decel:          accel * enemyShipBrakeFactor,
		escapeMaxSpeed: clientSpeedMax * enemyShipEscapeSpeedFactor * frameScale,
		escapeAccel:    accel * enemyShipEscapeAccelFactor,
	}
	sim.applySettings(defaultNpcSettings())
	return sim
}

// applySettings swaps in a new set of admin-chosen settings. It's called
// from the client's read goroutine when ships-go pushes an npcConfig, so
// it takes the same lock the tick loop does; everything it changes is read
// fresh on the next tick, which is what makes the panel feel immediate.
func (s *npcSimulator) applySettings(settings npcSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings = settings.sanitized()
	s.maxSpeed = s.settings.EnemyShipSpeed * s.frameScale
}

// currentSettings returns a copy of the settings in force, for logging.
func (s *npcSimulator) currentSettings() npcSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// playerCenter returns the middle of a player's ship. Player positions on
// the wire are the ship's top-left anchor, so aiming straight at them means
// aiming at a corner: harmless at long range, but a systematic miss up
// close, where being half a ship off is a large angular error.
func (s *npcSimulator) playerCenter(p PlayerData) (float64, float64) {
	ship, ok := s.shipsById[p.ShipId]
	if !ok {
		return float64(p.X), float64(p.Y)
	}
	offsetX, offsetY := ship.centerOffset()
	return float64(p.X) + offsetX, float64(p.Y) + offsetY
}

// tick advances every NPC's simulation by one step: spawns/moves black
// holes as before, plus drives the enemy ship (spawn/respawn, chase the
// nearest player, shoot, expire its own bullets). players is a snapshot of
// currently known player positions (see playerTracker).
func (s *npcSimulator) tick(players map[string]PlayerData) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	if len(players) > 1 && len(s.npcs) < s.settings.MaxBlackHoles && now.Sub(s.lastSpawn) > s.settings.blackHoleSpawnPeriod() {
		bh := spawnBlackHole(players, now)
		s.npcs[bh.Id] = bh
		s.lastSpawn = now
	}

	toRemove := make([]string, 0)
	for id, npc := range s.npcs {
		inTime := now.Sub(npc.spawnedAt) < blackHoleDuration

		switch {
		case inTime && npc.Scale < 1.0:
			npc.Scale += blackHoleScaleStep
		case !inTime && npc.Scale <= blackHoleScaleStep:
			toRemove = append(toRemove, id)
			continue
		case !inTime && npc.Scale > blackHoleScaleStep:
			npc.Scale -= blackHoleScaleStep
		}

		radians := npc.Direction * (math.Pi / 180)
		npc.X += npc.Speed * math.Cos(radians)
		npc.Y += npc.Speed * math.Sin(radians)
	}

	for _, id := range toRemove {
		delete(s.npcs, id)
	}

	s.tickEnemyShips(players, now)
	s.expireBullets(now)
}

// snapshot returns the full current NPC batch, ready to be sent to
// ships-go in a single npcUpdate message.
func (s *npcSimulator) snapshot() []NpcData {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]NpcData, 0, len(s.npcs)+len(s.enemyShips))
	for _, npc := range s.npcs {
		out = append(out, npc.NpcData)
	}
	for _, ship := range s.enemyShips {
		out = append(out, ship.NpcData)
	}
	return out
}

// tickEnemyShips keeps the fleet at the size the admin asked for, then
// advances each ship independently. Ships are simulated separately (own
// target, own throttle, own fire cooldown) rather than as a squadron: they
// end up spread out and attacking from different angles for free, and no
// per-ship decision can stall the others.
func (s *npcSimulator) tickEnemyShips(players map[string]PlayerData, now time.Time) {
	s.manageFleetSize(players, now)
	for _, ship := range s.enemyShips {
		s.tickEnemyShip(ship, players, now)
	}
}

// manageFleetSize spawns ships up to the configured count and despawns any
// excess after the count is lowered. A despawn is deliberately silent (no
// playerDied): nobody killed them, so an explosion and a kill-feed entry
// would be a lie. ships-vue drops any NPC missing from the batch, so
// leaving them out of the next snapshot is all that's needed.
func (s *npcSimulator) manageFleetSize(players map[string]PlayerData, now time.Time) {
	desired := s.settings.EnemyShips

	for id := range s.enemyShips {
		if len(s.enemyShips) <= desired {
			break
		}
		delete(s.enemyShips, id)
	}

	if len(s.enemyShips) >= desired || len(players) == 0 || len(s.ships) == 0 {
		return
	}
	// The respawn delay is only armed by a death, so the initial fleet
	// (and any increase made from the admin panel) appears immediately.
	if now.Before(s.enemyRespawnAt) {
		return
	}
	for len(s.enemyShips) < desired {
		ship := spawnEnemyShip(players, s.ships, now, s.settings.EnemyShipLife)
		s.enemyShips[ship.Id] = ship
	}
}

// tickEnemyShip advances one ship: escape a black hole if one is too
// close, otherwise chase and shoot at the nearest living player.
func (s *npcSimulator) tickEnemyShip(enemyShip *enemyShipState, players map[string]PlayerData, now time.Time) {

	// Everything below is reasoned about in ship centers, not the top-left
	// anchors that travel on the wire, so distances/angles stay correct at
	// close range where half-a-ship offsets dominate.
	selfOffsetX, selfOffsetY := enemyShip.ship.centerOffset()
	selfX := enemyShip.X + selfOffsetX
	selfY := enemyShip.Y + selfOffsetY
	selfRadius := enemyShip.radius()

	// Black holes are just as lethal to an NPC ship as to a player, so
	// escaping one always outranks hunting: while in danger the ship turns
	// its back on the black hole, burns full throttle away from it and
	// holds its fire (its guns point where it's heading, like a player's).
	bhX, bhY, bhEdge, bhFound := s.nearestBlackHole(selfX, selfY, selfRadius)
	dangerRange := float64(enemyShipBlackHoleFear)
	if enemyShip.fleeing {
		dangerRange = enemyShipBlackHoleSafe
	}
	if bhFound && bhEdge <= dangerRange {
		enemyShip.fleeing = true
		escapeAngle := math.Atan2(selfY-bhY, selfX-bhX)
		enemyShip.Rotate = float32(escapeAngle)
		enemyShip.Speed = math.Min(enemyShip.Speed+s.escapeAccel, s.escapeMaxSpeed)
		enemyShip.X += enemyShip.Speed * math.Cos(escapeAngle)
		enemyShip.Y += enemyShip.Speed * math.Sin(escapeAngle)
		s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
		return
	}
	enemyShip.fleeing = false

	target, targetId, found := s.nearestPlayer(selfX, selfY, players)
	if !found {
		// Nobody to chase, but gravity doesn't care.
		s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
		return
	}

	targetX, targetY := s.playerCenter(target)
	dx := targetX - selfX
	dy := targetY - selfY
	dist := math.Hypot(dx, dy)
	angle := math.Atan2(dy, dx)
	enemyShip.Rotate = float32(angle)

	// Accelerate while closing in, brake once inside standoff range, so the
	// ship's speed actually varies over time instead of snapping between a
	// fixed value and zero.
	if dist > enemyShipStandoff {
		enemyShip.Speed += s.accel
		if enemyShip.Speed > s.maxSpeed {
			enemyShip.Speed = s.maxSpeed
		}
	} else {
		enemyShip.Speed -= s.decel
		if enemyShip.Speed < 0 {
			enemyShip.Speed = 0
		}
	}
	if enemyShip.Speed > 0 {
		enemyShip.X += enemyShip.Speed * math.Cos(angle)
		enemyShip.Y += enemyShip.Speed * math.Sin(angle)
	}

	if dist <= enemyShipShootRange && now.Sub(enemyShip.lastShotAt) > s.settings.fireCooldown() {
		s.fireAt(enemyShip, targetId, target, now)
		enemyShip.lastShotAt = now
	}

	s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
}

// applyBlackHoleGravity drags the enemy ship towards every nearby black
// hole, and destroys it if it reaches one, using the exact same maths
// ships-vue runs for the local player (applyNpcImpacts). It's applied after
// the ship's own movement, mirroring the client, where the pull is layered
// on top of whatever the player's controls did that frame - which is what
// makes escaping a matter of out-running the pull rather than being
// immune to it.
//
// centerOffset*/radius are passed in because the caller already resolved
// them; positions are stored as the ship's top-left anchor.
func (s *npcSimulator) applyBlackHoleGravity(enemyShip *enemyShipState, offsetX, offsetY, radius float64) {
	x := enemyShip.X + offsetX
	y := enemyShip.Y + offsetY

	for _, npc := range s.npcs {
		if npc.Type != NpcTypes.BlackHole {
			continue
		}
		bhX, bhY, bhRadius := blackHoleBody(npc)
		centerDistance := math.Hypot(bhX-x, bhY-y)
		if centerDistance <= 0.0001 {
			s.killEnemyShipByBlackHole(enemyShip)
			return
		}
		edgeDistance := centerDistance - (radius + bhRadius)
		if edgeDistance > blackHolePullRange {
			continue
		}

		// The closer it gets, the harder it's pulled - never past the
		// black hole's center in a single step.
		safeDistance := math.Max(edgeDistance, 1)
		jumpStep := math.Min((blackHolePullFactor/safeDistance)*blackHolePullBoost*s.frameScale, centerDistance)
		angle := math.Atan2(bhY-y, bhX-x)
		x += math.Cos(angle) * jumpStep
		y += math.Sin(angle) * jumpStep

		if jumpStep >= centerDistance-0.0001 {
			enemyShip.X = x - offsetX
			enemyShip.Y = y - offsetY
			s.killEnemyShipByBlackHole(enemyShip)
			return
		}
	}

	enemyShip.X = x - offsetX
	enemyShip.Y = y - offsetY
}

// killEnemyShipByBlackHole announces the death through the same playerDied
// event a bullet kill uses, but with no killer: ships-go's handler looks up
// Players[""], finds nobody and credits no one, and ships-vue falls back to
// the black hole explosion animation (killedByEnemyShot needs a `from`).
func (s *npcSimulator) killEnemyShipByBlackHole(enemyShip *enemyShipState) {
	died := playerDiedMsg{
		PlayerId: enemyShip.Id,
		X:        float32(enemyShip.X),
		Y:        float32(enemyShip.Y),
	}
	if err := s.sender.sendPlayerDied(died); err != nil {
		log.Println("ships-npc: failed to announce enemy ship black hole death:", err)
	}
	delete(s.enemyShips, enemyShip.Id)
	s.enemyRespawnAt = time.Now().Add(enemyShipRespawnDelay)
}

// nearestBlackHole returns the center and edge distance of the black hole
// currently closest to a body of the given radius at (x, y) - "closest"
// being measured edge-to-edge, since a big black hole is dangerous from
// much further away than a small one.
func (s *npcSimulator) nearestBlackHole(x, y, radius float64) (bhX, bhY, edgeDistance float64, found bool) {
	for _, npc := range s.npcs {
		if npc.Type != NpcTypes.BlackHole {
			continue
		}
		cx, cy, bhRadius := blackHoleBody(npc)
		edge := math.Hypot(cx-x, cy-y) - (radius + bhRadius)
		if !found || edge < edgeDistance {
			bhX, bhY, edgeDistance, found = cx, cy, edge, true
		}
	}
	return
}

// blackHoleBody resolves a black hole's collision body the way ships-vue
// does: its Animation is a MaxSize square scaled by Scale, anchored at its
// top-left, and its radius is the half-diagonal of that square
// (getRealDimension + getRadiusFromRect).
func blackHoleBody(bh *npcState) (centerX, centerY, radius float64) {
	size := float64(bh.MaxSize) * bh.Scale
	return bh.X + size/2, bh.Y + size/2, (size / 2) * math.Sqrt2
}

// fireAt sends a newBullet event aimed at target's last known position,
// exactly like a player's client would, so every client renders/animates
// it identically with no ships-vue changes needed.
func (s *npcSimulator) fireAt(enemyShip *enemyShipState, targetId string, target PlayerData, now time.Time) {
	offsetX, offsetY := enemyShip.ship.centerOffset()
	originX := enemyShip.X + offsetX
	originY := enemyShip.Y + offsetY

	// Aim at the middle of the target's ship, not its top-left anchor: at
	// the standoff distance a half-ship offset is several degrees off, so
	// aiming at the corner made every close-range shot miss.
	targetX, targetY := s.playerCenter(target)
	dx := targetX - originX
	dy := targetY - originY
	angle := math.Atan2(dy, dx)
	// ships-vue's Bullet class (and every other angle sent over the wire,
	// e.g. a player's own rotate) expects angles normalized to [0, 2*Pi),
	// not atan2's (-Pi, Pi] range - its quadrant-sign-correction trick for
	// moveX/moveY silently breaks for negative angles otherwise.
	if angle < 0 {
		angle += 2 * math.Pi
	}
	moveX := math.Cos(angle)
	moveY := math.Sin(angle)

	id := enemyShip.Id + "-" + uuid.NewString()
	bullet := newBulletMsg{
		SocketId:      enemyShip.Id,
		Id:            id,
		Angle:         float32(angle),
		Rotation:      float32(angle),
		BulletCharge:  npcBulletCharge,
		ShootingSpeed: npcBulletShootingSpeed,
		X:             float32(originX),
		Y:             float32(originY),
		MoveX:         float32(moveX),
		MoveY:         float32(moveY),
		ExpX:          float32(moveX*npcBulletRange + originX),
		ExpY:          float32(moveY*npcBulletRange + originY),
	}
	if err := s.sender.sendBullet(bullet); err != nil {
		log.Println("ships-npc: failed to send enemy ship bullet:", err)
		return
	}
	s.activeBullets[id] = now
}

// expireBullets removes bullets this service fired once they've had enough
// time to travel their full range (see npcBulletLifetime).
func (s *npcSimulator) expireBullets(now time.Time) {
	for id, firedAt := range s.activeBullets {
		if now.Sub(firedAt) < npcBulletLifetime {
			continue
		}
		if err := s.sender.sendRemoveBullet(id); err != nil {
			log.Println("ships-npc: failed to remove expired bullet:", err)
		}
		delete(s.activeBullets, id)
	}
}

// handleNpcHit applies damage reported by a player's client (its bullet hit
// our enemy ship). If this kills it, credits the killer and starts a
// respawn timer via ships-go's existing playerDied handling.
func (s *npcSimulator) handleNpcHit(hit npcHitMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()

	enemyShip, ok := s.enemyShips[hit.NpcId]
	if !ok {
		return
	}

	// The shooter's client only removes its own logical/collision-tracking
	// copy of the bullet locally. The bullet that's actually rendered on
	// every screen (including the shooter's) is a separate copy fed by the
	// newBullet broadcast, and it's only ever cleared by an explicit
	// removeBullet event - mirroring how a hit player's client requests
	// this for player-vs-player hits. Without this, the bullet visually
	// keeps flying through the NPC even though it already dealt damage.
	if err := s.sender.sendRemoveBullet(hit.BulletId); err != nil {
		log.Println("ships-npc: failed to remove spent bullet:", err)
	}

	enemyShip.Life -= hit.BulletCharge
	if enemyShip.Life > 0 {
		return
	}

	died := playerDiedMsg{
		BulletId: hit.BulletId,
		From:     hit.From,
		PlayerId: enemyShip.Id,
		X:        float32(enemyShip.X),
		Y:        float32(enemyShip.Y),
	}
	if err := s.sender.sendPlayerDied(died); err != nil {
		log.Println("ships-npc: failed to announce enemy ship death:", err)
	}
	delete(s.enemyShips, enemyShip.Id)
	s.enemyRespawnAt = time.Now().Add(enemyShipRespawnDelay)
}

// nearestPlayer returns the tracked living player whose ship center is
// closest to (x, y), which is also expected to be a center. Dead players
// (self-reported via their own periodic state update, same as ships-vue
// tracks it) are never targeted so the enemy ship stops chasing and
// shooting once every player is dead.
func (s *npcSimulator) nearestPlayer(x, y float64, players map[string]PlayerData) (PlayerData, string, bool) {
	var best PlayerData
	var bestId string
	bestDist := math.MaxFloat64
	found := false
	for id, p := range players {
		if p.IsDead {
			continue
		}
		px, py := s.playerCenter(p)
		d := math.Hypot(px-x, py-y)
		if !found || d < bestDist {
			best, bestId, bestDist, found = p, id, d, true
		}
	}
	return best, bestId, found
}

func spawnEnemyShip(players map[string]PlayerData, ships []publicShip, now time.Time, maxLife float32) *enemyShipState {
	minX, minY, maxX, maxY := spawnBounds(players, enemyShipSpawnMargin)
	rangeX := maxX - minX
	rangeY := maxY - minY
	ship := ships[rand.Intn(len(ships))]

	return &enemyShipState{
		spawnedAt: now,
		ship:      ship,
		NpcData: NpcData{
			Type:    NpcTypes.Ship,
			Id:      uuid.NewString(),
			ShipId:  ship.Id,
			Name:    "Enemy " + ship.Name,
			X:       rand.Float64()*float64(rangeX) + float64(minX),
			Y:       rand.Float64()*float64(rangeY) + float64(minY),
			Scale:   enemyShipScale,
			Life:    maxLife,
			MaxLife: maxLife,
		},
	}
}

// spawnBounds returns a bounding box around the current players, expanded
// by margin, used to pick a random spawn point that's near the action but
// not right on top of anyone.
func spawnBounds(players map[string]PlayerData, margin float32) (minX, minY, maxX, maxY float32) {
	first := true
	for _, p := range players {
		if first {
			minX, minY, maxX, maxY = p.X, p.Y, p.X, p.Y
			first = false
			continue
		}
		minX = min(minX, p.X)
		minY = min(minY, p.Y)
		maxX = max(maxX, p.X)
		maxY = max(maxY, p.Y)
	}
	minX -= margin
	maxX += margin
	minY -= margin
	maxY += margin
	return
}

func spawnBlackHole(players map[string]PlayerData, now time.Time) *npcState {
	minX, minY, maxX, maxY := spawnBounds(players, blackHoleSpawnMargin)
	rangeX := maxX - minX
	rangeY := maxY - minY

	return &npcState{
		spawnedAt: now,
		NpcData: NpcData{
			Type:      NpcTypes.BlackHole,
			Id:        uuid.NewString(),
			X:         rand.Float64()*float64(rangeX) + float64(minX),
			Y:         rand.Float64()*float64(rangeY) + float64(minY),
			Scale:     blackHoleScaleStep,
			MaxSize:   800,
			Direction: rand.Float64() * 360,
			Duration:  int(blackHoleDuration.Milliseconds()),
			Speed:     blackHoleSpeed,
		},
	}
}
