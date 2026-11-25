# Temucraft e2e suites

Live tests for the Temucraft modules, written with the same AzerothGhost harness as the rest of `e2e/`. They log
bots in to a running Temucraft worldserver and check what the modules do through the real protocol and the databases.

| Folder | What it checks | Module |
|---|---|---|
| `commands` | `.temucraft hearthstone`, `.temucraft challenge` and `.temucraft shop` answer; `.temucraft` lists its subcommands; a command whose RBAC permission is denied to the account does nothing while the others keep working | mod-temucraft-travel, -challenges, -shop |
| `shop` | the Temucraft Curator's gossip: 8 "Browse:" categories, a category opens a vendor window with goods, the balance line, and (when the realm sells tokens for gold) the gold really buys a token | mod-temucraft-shop |
| `courier` | `.temucraft courier`: the courier's gossip opens the bank and the mailbox, another player is refused and gets no menu, a second call inside the cooldown is refused | mod-temucraft-courier |
| `reagentbag` | the reagent bag addon messages (prefix `TPG`): RG, RT, RD, RW, RA and the ER refusal, checked in the answer, the bags and `mod_temucraft_reagent_bag` | mod-temucraft-professions |
| `auras` | the cosmetic aura toys: on, saved, back after a relog and after death, one at a time, off and forgotten | mod-temucraft-auras (in mod-temucraft-courier) |
| `hearthstone` | the hearthstone choice by command (saved, kept after a relog, refused for minutes not offered), by innkeeper gossip, "no cooldown" clearing the cooldown after a cast, and the innkeeper choice (first choice, item 0) casting no Trick or Treat (24751/24755) and giving no items (3512616) | mod-temucraft-travel |
| `challenges` | Self-Found: joined at level 1 (aura and DB row), no trade and no mail both ways, can't be joined past level 1 | mod-temucraft-challenges |
| `reputation` | no faction_dbc row gives a reputation slot past 127; a Forsaken character talks to Sir Malory Wheeler and gains reputation, every slot the server sends is below 128, no Lost Lands faction rows come back, two relogs, world alive (the 2026-11-23 client and server crashes) | mod-temucraft-lostlands, core |
| `invasion` | `.temucraft invasion start`: a second player gets the "[Elemental Invasion]" announcement and the world stays up (crashes 023-027) | mod-temucraft-events |
| `channels` | a custom chat channel: ownership handed over, a moderator change for a non-member answered "not found", the owner dropping without a logout, chat afterwards, zone changes between Ironforge and Darnassus, world alive (crash 016) | core (Channel) |
| `debugcmds` | `.gps` gives exactly one ".go xyz" line for a GM level 3 account and for a security 0 account (role 1010 has permission 505), and none when 505 is denied; a security 0 account gets answers from `.lookup item`, `.distance`, `.mmap loc` and `.list auras`, and none from `.lookup player ip` | mod-temucraft, mod-ryans-debug |
| `pools` | pooled gathering nodes (gameobject types 3 and 25) of two always-on pools on maps 0/1 are live where the pool puts them (core 9e482708e, Map::InitPools after PoolMgr::LoadFromDB) | core |
| `firstmount` | quests 602140-602159 each allow exactly one race (and the race of `temucraft_first_mount_race`); Kar Stormsinger offers a Tauren the Tauren quest and none of 602140-602159 | mod-temucraft-first-mount |
| `classes` | no `conditions` row ties Parachute (55001) to Engineering, and a level 10 character without it casts 55001; a Tauren logging in twice more is neither taught nor stripped of Earthmother's Embrace (100176) | mod-temucraft-classes |
| `merchants` | a Sealed Apprentice Crate turned in (quest 602010) is taken and gives Merchant's Favor (601700) and reputation with 2233 or 2234; no two `currencytypes_dbc` rows share a BitIndex and the Favor's is 1-64 | mod-temucraft-merchants |

`tcwire` holds the shared helpers: a reader for system messages, notifications and addon whispers
(`SMSG_MESSAGECHAT`, `SMSG_NOTIFICATION`), gossip (`CMSG_GOSSIP_HELLO` / `SELECT_OPTION`, `SMSG_GOSSIP_MESSAGE`),
item use, addon messages, mail, and the database checks. `quests.go` reads the quests a quest giver offers and spell
casts; `sessions.go` turns an account into a player account (`AsPlayer`) and logs a character in on a client hooked
before it enters the world (`LoginObserved`). The harness has none of these, so they build the 3.3.5a
packets themselves and read replies with `AddPacketHook`.

## What you need

1. A Temucraft realm you can throw away (the tests make accounts and characters, spawn an innkeeper, write settings).
   All the modules above built in, and their SQL applied (world, characters and auth).
2. The rest of the e2e setup from [`e2e/README.md`](../../README.md): auth + world + MySQL, `Warden.Enabled = 0`,
   and Go 1.26 or newer.
3. Default module settings, or at least:
   - `Temucraft.Travel.Hearthstone.Choices` holds 0, 15 and 45 and `AllowChoice = 1`;
   - `Temucraft.Challenges.SelfFound.Enable`, `.NoTrade` and `.NoMail` = 1 and `Temucraft.Challenges.AuraFirst = 95410`;
   - `Temucraft.Shop.Summon = 1` (the curator test calls it with `.temucraft shop summon`);
   - `Temucraft.Courier.Enable = 1`;
   - `Temucraft.Events.Invasion.Enable = 1` and `Temucraft.Events.Invasion.Announce = 1` (the invasion test).
4. The harness accounts are GM level 3, which is the administrator role (192) in AzerothCore. Every `.temucraft`
   command is linked to 192 and 1014, so they can use all of them.

## How to run

From `e2e/`, with the `E2E_*` variables of `.env.example` exported:

```bash
# all Temucraft suites
go test -tags=e2e ./suites/temucraft/... -count=1 -v -timeout 60m -parallel 1 -p 1
# or
make e2e-category C=temucraft

# one suite / one test
make e2e-sub C=temucraft/reagentbag
go test -tags=e2e ./suites/temucraft/hearthstone -run TestHearthstone_InnkeeperGossip -count=1 -v -timeout 15m
```

Keep `-p 1`: each folder takes its own isolation pad, and two packages at once can share one.

## Notes

- `TestTemucraft_CommandDeniedByRBAC` writes a deny row for permission 103350 to `rbac_account_permissions` of the
  test account and deletes it at the end. If a run is killed in the middle, delete that row by hand.
- `TestHearthstone_NoCooldownClearsAfterCast` really casts the Hearthstone (10 seconds) and comes back to its pad.
- The courier, curator and challenges tests need two bots or summons in the same spot; they place everyone on the
  suite's pad with `TeleportAllPad`.
- What is not covered here: the courier's item helpers (Courier's Bell and the others), the reagent bag's RP
  (it needs a learned recipe with reagents), the curator's restore of deleted characters, and the Self-Found auction
  house rule (the harness can't open an auctioneer window).
- `TestInvasion_AnnouncementReachesEveryone` really starts an invasion (rifts in four zones) and stops it at the end,
  which also schedules the next one. Run it on a realm where that doesn't matter.
- `reputation`, `invasion` and `channels` (added 2026-11-23) were checked with `go vet -tags=e2e` and
  `go test -tags=e2e -c` only.
- `debugcmds` and `TestPlayerDebug_*` set the test account to security 0 in `account_access` (and back to 3 at the end),
  and `TestGps_DeniedGetsNoGoXyzLine` writes a deny row for permission 505; delete them by hand if a run is killed.
  They need mod-temucraft's auth SQL (`temucraft_rbac_roles.sql`). The `.gps` tests are skipped when neither
  mod-temucraft nor mod-ryans-debug has `module_string` rows. The `.go xyz` line is a delayed player event, so the
  `.gps` tests count lines for 1.5 s after the first one.
- `TestPools_GatheringNodesSpawnOnContinents` teleports to every location of the pools it checks (up to 16 each);
  a node that was just gathered is only a warning, a pool with no live node fails.
- `TestHearthstone_InnkeeperChoiceNoTrickOrTreat` needs an innkeeper whose SmartAI casts 24751 on a gossip pick
  (stock world DB) and the default choices (it sets 45 first and picks the first choice).
- `TestRacialHeal_NoRelearnOnLogin` needs `Temucraft.ClassChanges.RacialHeals = 1` (default).
- Not covered: a clash of a `currencytypes_dbc` bit with a stock CurrencyTypes.dbc row (the server's DBC file is not
  in the database), and the client-side "You have learned" text itself (the tests read the packet that makes it).
- `debugcmds`, `pools`, `firstmount`, `classes`, `merchants` and the Trick or Treat test (added 2026-11-25) were
  checked with `go vet -tags=e2e` and `go build -tags=e2e` only.
- These suites were checked with `go vet -tags=e2e` and `go build -tags=e2e` only. They have not run against a live
  realm yet, so the first run may need small fixes (for example a module text that differs from the fallback text a
  test looks for).
