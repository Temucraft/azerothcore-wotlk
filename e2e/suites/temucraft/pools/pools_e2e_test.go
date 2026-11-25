//go:build e2e

package pools_test

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/azerothcore/AzerothGhost/e2e/e2eharness"
	"github.com/azerothcore/azerothcore-wotlk/e2e/internal/meta"
)

const (
	poolsToCheck = 2
	maxMembers   = 16 // pools with more locations are skipped, to keep the walk short
	// after a teleport the node's create packet comes with the grid; a missing node is given this long
	nodeWait = 4 * time.Second
	nodeNear = 3 // yards between the live node and its gameobject row
)

type node struct {
	guid, entry, mapID uint32
	x, y, z            float32
}

type pool struct {
	entry, maxLimit uint32
	desc            string
	nodes           []node
}

// Top-level gathering pools of the continents: mining and herb nodes (gameobject type 3) and fishing pools (25), on
// maps 0 and 1, not a child of another pool and not tied to a game event, so they are always spawned. Copper veins
// first (the reported case), then by entry.
func gatheringPools(t *testing.T, db *sql.DB) []pool {
	t.Helper()
	rows, err := db.Query("SELECT pt.`entry`, pt.`max_limit`, COALESCE(pt.`description`, '') FROM `pool_template` pt " +
		"WHERE pt.`max_limit` > 0 " +
		"AND pt.`entry` NOT IN (SELECT `pool_id` FROM `pool_pool`) " +
		"AND pt.`entry` NOT IN (SELECT `pool_entry` FROM `game_event_pool`) " +
		"AND pt.`entry` IN (SELECT pg.`pool_entry` FROM `pool_gameobject` pg JOIN `gameobject` g ON g.`guid` = pg.`guid` " +
		"JOIN `gameobject_template` gt ON gt.`entry` = g.`id` WHERE g.`map` IN (0, 1) AND gt.`type` IN (3, 25) AND g.`phaseMask` & 1) " +
		"ORDER BY (pt.`description` LIKE '%Copper%') DESC, pt.`entry` LIMIT 40")
	if err != nil {
		e2eharness.HarnessFailf(t, "pool_template: %v", err)
	}
	var candidates []pool
	for rows.Next() {
		var p pool
		if rows.Scan(&p.entry, &p.maxLimit, &p.desc) == nil {
			candidates = append(candidates, p)
		}
	}
	rows.Close()

	var out []pool
	for _, p := range candidates {
		nrows, err := db.Query("SELECT g.`guid`, g.`id`, g.`map`, g.`position_x`, g.`position_y`, g.`position_z` FROM `pool_gameobject` pg "+
			"JOIN `gameobject` g ON g.`guid` = pg.`guid` WHERE pg.`pool_entry` = ? ORDER BY g.`guid`", p.entry)
		if err != nil {
			e2eharness.HarnessFailf(t, "pool_gameobject %d: %v", p.entry, err)
		}
		for nrows.Next() {
			var n node
			if nrows.Scan(&n.guid, &n.entry, &n.mapID, &n.x, &n.y, &n.z) == nil {
				p.nodes = append(p.nodes, n)
			}
		}
		nrows.Close()
		if len(p.nodes) < 2 || len(p.nodes) > maxMembers {
			continue
		}
		out = append(out, p)
		if len(out) == poolsToCheck {
			break
		}
	}
	return out
}

// nodeVisible waits for a live gameobject of the node's entry at the node's position.
func nodeVisible(bot *e2eharness.ScenarioBot, n node) bool {
	deadline := time.Now().Add(nodeWait)
	for time.Now().Before(deadline) {
		if g := bot.World.FindGameObjectByEntry(n.entry, 8); g != 0 {
			if o := bot.World.GetObject(g); o != nil && o.HasKnownPosition() &&
				math.Hypot(float64(o.PosX-n.x), float64(o.PosY-n.y)) <= nodeNear {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// Core 9e482708e: maps made while the spawns load (the continents) got their pool list before PoolMgr::LoadFromDB
// had run, so no pooled spawn ever came up on them: no mining or herb nodes, no fishing pools. Map::InitPools now
// runs again once the pools are loaded.
//
// For two always-on gathering pools of Eastern Kingdoms or Kalimdor, the bot visits each location until it has seen
// the pool's max_limit nodes. At least one live node per pool is the oracle (a node someone just gathered respawns
// elsewhere later, so fewer than max_limit is only a warning).
func TestPools_GatheringNodesSpawnOnContinents(t *testing.T) {
	meta.Begin(t, meta.TestMeta{Tags: []string{"med", "temucraft", "pools", "world"}, Runtime: "med", Category: "temucraft/pools"})

	db, err := e2eharness.OpenWorldDB()
	if err != nil {
		e2eharness.Preconditionf(t, "world DB: %v", err)
	}
	defer db.Close()
	pools := gatheringPools(t, db)
	if len(pools) == 0 {
		e2eharness.Preconditionf(t, "no always-on gathering pool on maps 0/1 with 2-%d locations", maxMembers)
	}

	bot := e2eharness.NewSolo(t, e2eharness.ScenarioOpts{Prefix: "TcPool", Level: 80})
	pad := e2eharness.PackagePad(t)
	bot.TeleportPad(t, pad)

	for _, p := range pools {
		var seen []string
		for _, n := range p.nodes {
			if uint32(len(seen)) >= p.maxLimit {
				break
			}
			bot.Teleport(t, n.x, n.y, n.z+2, n.mapID)
			if nodeVisible(bot, n) {
				seen = append(seen, fmt.Sprintf("%d", n.guid))
			}
		}
		t.Logf("pool %d %q: %d of max %d nodes live (spawns %s) among %d locations", p.entry, p.desc, len(seen), p.maxLimit, strings.Join(seen, ","), len(p.nodes))
		if len(seen) == 0 {
			e2eharness.Assertf(t, "pool %d %q: none of its %d locations has a live node (entry %d); pooled spawns are missing on map %d",
				p.entry, p.desc, len(p.nodes), p.nodes[0].entry, p.nodes[0].mapID)
		}
		if uint32(len(seen)) < p.maxLimit {
			e2eharness.SoftWarnf(t, "pool %d: %d of %d nodes live (some may have been gathered)", p.entry, len(seen), p.maxLimit)
		}
	}
	bot.TeleportPad(t, pad)
	t.Logf("PASS pooled gathering nodes are spawned on the continents")
}
