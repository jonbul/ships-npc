package main

import (
	"strings"
	"testing"
	"time"
)

func matrixSim(f *fakeSender, mutate func(*npcSettings)) *npcSimulator {
	settings := defaultNpcSettings()
	settings.EnemyShipController = controllerBoth
	settings.EnemyShips = 0
	settings.AiShips = 0
	settings.MaxBlackHoles = 0
	mutate(&settings)
	return settingsSim(f, settings)
}

// The admin's controller choice decides which fleets exist at all, and the
// two counts are independent: turning one fleet off must not disturb the
// other's size.
func TestControllerChoiceSizesEachFleetIndependently(t *testing.T) {
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 0, Y: 0, Width: 100, Height: 200, Scale: 1}}

	cases := []struct {
		controller           string
		wantRule, wantAiShip int
	}{
		{controllerNone, 0, 0},
		{string(controllerRule), 3, 0},
		{string(controllerAi), 0, 5},
		{controllerBoth, 3, 5},
	}

	for _, tc := range cases {
		t.Run(tc.controller, func(t *testing.T) {
			s := matrixSim(&fakeSender{}, func(settings *npcSettings) {
				settings.EnemyShipController = tc.controller
				settings.EnemyShips = 3
				settings.AiShips = 5
			})
			s.mu.Lock()
			s.manageFleetSize(players, time.Now())
			rule, ai := s.fleetCount(controllerRule), s.fleetCount(controllerAi)
			s.mu.Unlock()

			if rule != tc.wantRule || ai != tc.wantAiShip {
				t.Fatalf("controller %q: %d rule / %d AI ships, want %d / %d",
					tc.controller, rule, ai, tc.wantRule, tc.wantAiShip)
			}
		})
	}
}

// A player has to be able to tell the fleets apart on sight, which is the
// whole point of running both: the tag is in the name, so it shows on the
// ship and in the scoreboard without any client change.
func TestShipNamesCarryTheirFleetTag(t *testing.T) {
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", Width: 100, Height: 200, Scale: 1}}
	s := matrixSim(&fakeSender{}, func(settings *npcSettings) {
		settings.EnemyShips = 2
		settings.AiShips = 2
	})
	s.mu.Lock()
	s.manageFleetSize(players, time.Now())
	defer s.mu.Unlock()

	seen := map[string]bool{}
	for _, ship := range s.enemyShips {
		want := "[NPC]"
		if ship.controller == controllerAi {
			want = "[AI]"
		}
		if !strings.HasPrefix(ship.Name, want) {
			t.Fatalf("ship flown by %q is named %q, want the %s tag", ship.controller, ship.Name, want)
		}
		if seen[ship.Name] {
			t.Fatalf("two ships share the name %q; a fleet of a hundred would be unreadable", ship.Name)
		}
		seen[ship.Name] = true
	}
}

// The attack matrix is the feature: any combination has to be expressible,
// including a fleet that fights itself and one that attacks nothing.
func TestAttackMatrix(t *testing.T) {
	all := func(settings *npcSettings) {
		settings.NpcAttacksPlayers = true
		settings.NpcAttacksNpc = true
		settings.NpcAttacksAi = true
		settings.AiAttacksPlayers = true
		settings.AiAttacksNpc = true
		settings.AiAttacksAi = true
	}

	s := matrixSim(&fakeSender{}, all)
	for _, attacker := range allControllers {
		for _, target := range []faction{factionPlayers, controllerRule.faction(), controllerAi.faction()} {
			if !s.settings.attacks(attacker, target) {
				t.Fatalf("everything on: %s should attack %s", attacker, target)
			}
		}
	}

	// One-sided: the AI is hunted and never shoots back.
	oneSided := matrixSim(&fakeSender{}, func(settings *npcSettings) {
		settings.NpcAttacksAi = true
	})
	if !oneSided.settings.attacks(controllerRule, controllerAi.faction()) {
		t.Fatal("rule fleet should hunt the AI fleet")
	}
	if oneSided.settings.attacks(controllerAi, controllerRule.faction()) {
		t.Fatal("AI fleet should not shoot back")
	}

	// A disabled fleet attacks nothing and is attacked by nobody, so
	// bullets already in flight stop counting the moment it is switched off.
	off := matrixSim(&fakeSender{}, func(settings *npcSettings) {
		settings.EnemyShipController = string(controllerRule)
		all(settings)
	})
	if off.settings.attacks(controllerAi, factionPlayers) {
		t.Fatal("a switched-off fleet still attacked players")
	}
	if off.settings.attacks(controllerRule, controllerAi.faction()) {
		t.Fatal("a switched-off fleet was still a valid target")
	}
}

// Targeting has to obey the matrix, not just the settings struct: a ship
// must ignore a rival it is not allowed to attack even when that rival is
// far closer than anything it may attack.
func TestTargetingObeysTheMatrix(t *testing.T) {
	s := matrixSim(&fakeSender{}, func(settings *npcSettings) {
		settings.NpcAttacksPlayers = true
	})
	ship := s.ships[0]
	hunter := &enemyShipState{controller: controllerRule, ship: ship, NpcData: NpcData{
		Type: NpcTypes.Ship, Id: "hunter", ShipId: ship.Id, Scale: 1, Life: 10, MaxLife: 10,
	}}
	s.enemyShips["hunter"] = hunter
	s.enemyShips["bystander"] = &enemyShipState{controller: controllerAi, ship: ship, NpcData: NpcData{
		Type: NpcTypes.Ship, Id: "bystander", ShipId: ship.Id, X: 100, Y: 0, Scale: 1, Life: 10, MaxLife: 10,
	}}
	players := map[string]PlayerData{"p1": {
		SocketId: "p1", ShipId: ship.Id, X: 5000, Y: 0, Width: 100, Height: 200, Scale: 1,
	}}

	target, found := s.nearestTarget(hunter, 0, 0, players)
	if !found || target.id != "p1" {
		t.Fatalf("hunted %+v, want the distant player: the near AI ship is off limits", target)
	}

	// Allow it, and the nearer AI ship wins.
	settings := s.settings
	settings.NpcAttacksAi = true
	s.settings = settings
	if target, found = s.nearestTarget(hunter, 0, 0, players); !found || target.id != "bystander" {
		t.Fatalf("hunted %+v, want the nearby AI ship once allowed", target)
	}
}

// Damage must obey the matrix too, not only target selection: a stray
// bullet has to pass harmlessly through a ship its owner may not attack,
// or a one-sided matrix quietly becomes crossfire.
func TestBulletsOnlyDamageWhatTheirOwnerMayAttack(t *testing.T) {
	fire := func(allow bool) float32 {
		f := &fakeSender{}
		s := matrixSim(f, func(settings *npcSettings) { settings.NpcAttacksAi = allow })
		ship := s.ships[0]
		victim := &enemyShipState{controller: controllerAi, ship: ship, NpcData: NpcData{
			Type: NpcTypes.Ship, Id: "victim", ShipId: ship.Id, X: 200, Y: 0, Scale: 1, Life: 10, MaxLife: 10,
		}}
		s.enemyShips["victim"] = victim

		now := time.Now()
		s.activeBullets["b1"] = &npcBullet{
			ownerId: "shooter", ownerController: controllerRule,
			x: 0, y: 100, stepX: 400, stepY: 0, firedAt: now,
		}
		s.advanceBullets(now)
		return victim.Life
	}

	if life := fire(false); life != 10 {
		t.Fatalf("bullet damaged a ship its fleet may not attack: life %v", life)
	}
	if life := fire(true); life >= 10 {
		t.Fatalf("bullet did not damage a ship its fleet may attack: life %v", life)
	}
}
