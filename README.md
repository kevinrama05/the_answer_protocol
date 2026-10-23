*This activity has been created as part of the 42 curriculum by kerama*

# TAP: The Answer Protocol

## Description

TAP is a small multiplayer text adventure, a modern take on the MUDs (Multi-User
Dungeons) of the 80s and 90s. Several players connect to one shared world, the
city of **Novaris** and the ruins buried under it. They explore rooms, pick up
and trade unique items, talk to NPCs, take quests, fight enemies, form groups
and chat with each other in real time.

The project has three programs, all written in **Go** (standard library only):

| Component  | Path              | What it does                                                                 |
|------------|-------------------|------------------------------------------------------------------------------|
| Server     | `server/`, `internal/` | TCP server that implements RFC 42TAP and holds the shared world state.  |
| CLI client | `client/`         | Terminal client. Takes raw protocol commands and friendly shortcuts.         |
| GUI client | `gui/`            | Graphical client: a local web UI served and bridged by a Go program.         |
| World data | `world/world.json`| Static world: 45 rooms, 25 items, 21 NPCs, 6 quests.                          |

The server and the clients speak a line-based protocol over TCP (UTF-8, one
message per `\n`-terminated line), defined in the RFC 42TAP document that
ships with the subject.

## Instructions

Requirements: **Go ≥ 1.22** and `make`. Nothing else: there are no third-party
modules, and the GUI only needs a web browser.

```sh
make install          # checks Go, downloads/verifies modules (none needed)
make build            # bin/tap-server, bin/tap-client, bin/tap-gui
make run-server       # listens on :4242 (PORT=… WORLD=… LOG_FILE=…)
make run-client       # CLI client  (HOST=… PORT=… NAME=alice)
make run-client-gui   # GUI client  (HOST=… PORT=… GUI_ADDR=127.0.0.1:8080)
```

Details for every target are in [Building and Running](#building-and-running).

---

## Architecture

```
            ┌──────────────────── tap-server ─────────────────────┐
 TCP ─────▶ │ internal/server   one goroutine per connection        │
 clients    │   • line framing (fragmentation / coalescing)        │
            │   • limits: 1024 B/line, rate limit, conn limits     │
            │   • bounded send queue + writer goroutine per client │
            │                │ game.Handle(player, cmd, args)      │
            │                ▼                                     │
            │ internal/game     dispatcher: map[CMD]handler        │
            │   • one mutex around the whole world state           │
            │   • handlers return the reply + queue events         │
            │ internal/world    JSON loading + reference checks    │
            └──────────────────────────────────────────────────────┘
```

**Dispatcher / router.** `internal/game/game.go` holds a `map[string]handler`
from the upper-cased command name to a method (`cmdLook`, `cmdMove`, …). Each
handler gets the player, the raw argument string and an *outbox*. It returns
exactly one reply line (`OK …` / `ERR …`) and adds events to the outbox. The
dispatcher sends the reply first, then the events, so a client always sees `OK`
before the `EVT` its own command caused (for example `CHAT GLOBAL`). `CONNECT`
and `QUIT` belong to the connection lifecycle and are handled in
`internal/server`.

**Concurrency model.**
* Each TCP connection has a **reader goroutine** that frames lines and calls the
  game, and a **writer goroutine** that drains a bounded queue (512 lines).
* The game state is protected by a **single mutex**. A command runs as one
  critical section, so the world is never seen half-updated (for example an item
  in two inventories). Commands are tiny, so contention is not an issue at MUD
  scale, and the model is simple to reason about.
* `Send` never blocks. It only pushes into the client's queue. A client that
  stops reading and fills its queue, or whose socket write fails, is
  disconnected (logged as `SEND_FAILED`, code 901). Broadcasts to everyone else
  carry on uninterrupted, even if a client disconnects mid-send.
* Event recipients are resolved when the event is created (inside the critical
  section). That is how "LEAVE to the old room, ENTER to the new room" is exact.
* On disconnect (QUIT, EOF, error, flood) the player is **removed from the
  world first**: room, player list, group, combat, and held items dropped on the
  floor. Only then are `EVT ROOM PRESENCE LEAVE`, `EVT GROUP LEAVE` and
  `EVT STATS` broadcast.
* Enemy respawns use `time.AfterFunc` and take the same mutex.

**World loading.** `internal/world` decodes `world/world.json` with unknown
fields rejected, then checks every reference. This covers exits to existing
rooms, room ↔ NPC consistency, items, quest givers, objectives, rewards,
prerequisites and enemy stats. It also warns about one-way exits and rooms
unreachable from the spawn. The server refuses to start on any error and lists
all of them. `make check-world` runs the validation alone.

## Protocol Implementation

The server implements every command and event in RFC 42TAP: CONNECT, LOOK,
MOVE, CHAT, WHO, GROUP CREATE/INVITE/JOIN/LEAVE, TAKE, DROP, INVENTORY, TALK,
ATTACK, STATUS, QUEST, QUESTS, QUIT, plus the ROOM/GLOBAL/GROUP/STATS events.
It uses the RFC reply formats exactly (`OK room=…`, `OK taken=…`, `OK players=N`,
`OK group=…`, JSON for LOOK/INVENTORY/STATUS/ATTACK/QUEST/QUESTS).

Choices where the RFC is silent or ambiguous:

| Topic | Choice | Why |
|---|---|---|
| Greeting | `OK hello proto=1` sent on accept | RFC §3.2 |
| Command names | case-insensitive; arguments for scopes/directions too | RFC §4.2 |
| Line endings | `\n`; a trailing `\r` is tolerated | robustness with telnet/nc |
| WHO | `OK players=<count>` (RFC §5.2.2) | The subject's example shows `{"room":[…],"server":N}`; we follow the RFC because it is normative. **Both clients accept both forms.** The room count comes from LOOK. |
| TALK | `OK <dialogue text>` (RFC §5.4.4) | The subject's example shows JSON; both clients accept both forms. Dialogue lines cycle per player. |
| Names | items/NPCs match by canonical ID (`item.healing_herbs`), ID without prefix (`healing_herbs`), display name (`Healing Herbs`, any case) or words of it (`herbs`) | RFC §8.3, multi-word names (§8.4) |
| Usernames | 1–24 Unicode letters/digits/`_`/`-`, unique **case-insensitively** | avoids `alice`/`Alice` impersonation; control characters rejected (§9.2) |
| Group IDs | `grp.<n>`; JOIN takes the leader name (RFC) **or** the group ID; joining needs an invitation | prevents strangers from joining |
| Group leave | leadership passes to the oldest member; an empty group is dissolved | |
| Chat text | control characters stripped (§9.2), forwarded otherwise unchanged | |
| QUEST | `QUEST <npc>` offers **and accepts** in one step, so replies show `status: active` (then `completed` on turn-in) rather than the RFC example's `available` | one command fewer; see Quest System |

**Additional error codes** (the RFC list is not exhaustive. Codes stay in the
families of the RFC: 1xx syntax/session, 2xx identity, 4xx game rules):

| Code | Message | When |
|---|---|---|
| 100 | `UNKNOWN_COMMAND` | command name not recognised |
| 101 | `INVALID_ARGUMENTS` | missing/extra arguments, bad chat scope |
| 102 | `NOT_AUTHENTICATED` | any command other than CONNECT/QUIT before CONNECT |
| 103 | `ALREADY_AUTHENTICATED` | second CONNECT on one connection |
| 104 | `LINE_TOO_LONG` | line > 1024 bytes (RFC §9.4); the whole line is discarded |
| 105 | `RATE_LIMITED` | command flood (see Server Logging) |
| 106 | `INVALID_ENCODING` | line is not valid UTF-8 |
| 202 | `INVALID_NAME` | username fails validation |
| 403 | `PLAYER_NOT_FOUND` | GROUP INVITE/JOIN of an unknown player |
| 407 | `NOT_INVITED` | GROUP JOIN without an invitation |
| 408 | `ITEM_NOT_OBTAINABLE` | TAKE on scenery (e.g. *Vault Seal*) |
| 409 | `INVENTORY_FULL` | more than 20 items (RFC §9.4 "resource acquisition") |
| 410 | `NOT_IN_COMBAT` | DEFEND/FLEE outside combat |
| 411 | `ITEM_NOT_USABLE` | USE on a non-consumable |
| 900 | `CONNECTION_FAILED` | (RFC code) sent before closing on server full / rapid reconnects |

**Additional commands:** `DEFEND`, `FLEE`, `USE <item>` (combat, see below).

**Additional events.** They follow the `EVT <category> <type> <data>` grammar.
Clients that don't know them can just log them:

| Event | Sent to | Meaning |
|---|---|---|
| `EVT ROOM ITEM TAKEN <user> <item-id>` | others in room | item left the floor |
| `EVT ROOM ITEM DROPPED <user> <item-id>` | others in room | item put down (also on disconnect) |
| `EVT ROOM COMBAT <json>` | others in room | a combat round (same JSON as ATTACK + `attacker`) |
| `EVT ROOM NPC DEFEATED <npc-id> <user>` | others in room | enemy killed |
| `EVT ROOM NPC SPAWN <npc-id>` | room | enemy respawned |
| `EVT COMBAT HIT <json>` | the player | parting strike when walking away from a fight |
| `EVT PLAYER RESPAWN room=<id> hp=<n>` | the player | player died and respawned |
| `EVT QUEST PROGRESS <json>` | the player | objective count changed |
| `EVT QUEST COMPLETE <json>` | the player | quest completed, rewards granted |

**Client interoperability.** Both clients only rely on the RFC: they match each
`OK`/`ERR` with the oldest command still waiting for a reply, parse JSON when
present, and fall back to showing the raw line. They work against other groups'
servers (WHO/TALK variants included). Our server works with any RFC client.

## Combat System

Combat is **turn-based**. One combat command is one round, and the **acting
player always has the initiative**: the player strikes first, then the enemy
answers (if still alive). Several players can fight the same enemy. Its HP is
shared world state, and it counter-attacks whoever just acted.

| Command | Effect in the round |
|---|---|
| `ATTACK <npc>` | You deal damage; the enemy counter-attacks. Starts/continues combat. Non-enemies → `405 NPC_NOT_HOSTILE`. |
| `DEFEND` | You don't attack; the enemy's raw damage is **halved** and your armor value **doubled** for that hit. |
| `FLEE` | 60 % chance to escape through a random exit (no damage). On failure the enemy gets a full hit. |
| `USE <item>` | Consume a healing item (Fresh Bread +10, Healing Herbs +15, Healers Potion +40). In combat it costs your turn (enemy hits back). |
| `MOVE` while fighting | allowed, but the enemy lands a **parting strike** (never lethal), sent as `EVT COMBAT HIT`. |

**Formulas**
```
player damage  = B + rand(0 … B/2),   B = 5 + best weapon damage carried
critical hit   = 10 % chance, damage × 2
enemy damage   = D + rand(0 … D/4),   D = enemy "damage" stat
enemy miss     = 15 % chance (0 damage)
damage taken   = max(1, enemy damage − armor)    armor = best shield + best armor carried
DEFEND         = enemy damage ÷ 2, armor × 2
```
Weapons: Training Sword 4, Bandit Dagger 7, Blacksmith Hammer 8, Guard Sword 12.
Shields: Bandit Buckler 2, Iron Shield 4.

**States.** `STATUS` returns `{"hp","max_hp","status","in_combat","target","target_hp","room"}`.
`status` is `combat` while fighting, otherwise `healthy` (≥ 70 %), `wounded`
(≥ 30 %) or `critical`. The `ATTACK`/`DEFEND`/`FLEE`/`USE` reply is
`{"attacker_hp","target_hp","damage","status",…}`, with `status` one of
`combat`, `victory`, `fled`, `defeated`.

**Death and respawn.** A player at 0 HP respawns in the **Temple Hall** (safe
room) with **50/100 HP** and keeps their inventory. The room sees them leave
and arrive, and the player receives `EVT PLAYER RESPAWN`.

**Enemies.** Each enemy has its own HP and damage (rat 25/5 up to the Vault
Sentinel 120/18). A defeated enemy disappears, drops its loot on the floor and
**respawns after 60 s** (`-respawn` flag). If nobody is fighting an enemy any
more (everyone fled, died or left), it heals back to full, which stops
hit-and-run tactics.

**Why this design.** The initiative rule and the one-round-per-command model
need no timers or global turn order. That fits a text protocol where players
act at their own pace, and it is fully deterministic from the server's point
of view. DEFEND, FLEE and USE give real tactical choices (tank a big hit, run
away, heal) without adding a stats system. Shared enemy HP makes cooperation in
groups worthwhile.

All combat rounds are logged (`"type":"combat"`) and broadcast to the room
(`EVT ROOM COMBAT`).

## Quest System

Quests are defined in the world file: a giver NPC, an ordered list of
objectives, rewards, and optional prerequisites.

```json
"quest.smugglers_manifest": {
  "giver": "npc.dockmaster_ren",
  "objectives": [
    {"type": "defeat_npc",  "npc":  "npc.smuggler_vex"},
    {"type": "obtain_item", "item": "item.smugglers_manifest"},
    {"type": "talk_to",     "npc":  "npc.dockmaster_ren"}
  ],
  "rewards": ["item.warehouse_key", "item.novaris_coins"]
}
```

**Progression.**
1. `QUEST <npc>` on a quest giver **accepts** the quest (`status: active`,
   `progress: 0/N`). Without a quest, or with an unmet prerequisite, or once
   completed → `ERR 406 NO_QUEST_AVAILABLE`.
2. Objectives are tracked per player:
   * `obtain_item` is checked against the **live inventory**. Dropping the item
     undoes the progress.
   * `defeat_npc` is set when the enemy dies. **Every player fighting it and
     their group mates in the same room** get the credit.
   * `talk_to` is satisfied by `TALK <npc>` (or `QUEST <giver>`) **only when all
     previous objectives are done**. This turns "obtain X, talk to Y" into a
     real delivery.
3. After any relevant action (TAKE, DROP, TALK, QUEST, kill, USE) the server
   re-evaluates active quests and sends `EVT QUEST PROGRESS {…}` when the count
   changes.

**Completion and validation.** A quest completes **automatically** the moment
all objectives are satisfied. That usually happens when you talk to the
recipient with the items in your bag. The server then **consumes the delivered
items**, adds the rewards to the inventory and sends
`EVT QUEST COMPLETE {…}`. If it was completed by `QUEST <giver>`, the reply
also shows `status: completed`. `QUESTS` lists every accepted quest with
status and progress.

**Rewards** are new item instances created by the quest giver, so finishing a
quest never steals an item from another player. Rewards can exceed the
20-item limit, so a reward is never lost.

| Quest | Giver (room) | Objectives | Rewards |
|---|---|---|---|
| Herbal Remedy | Healer Aria (Healer's Room) | get Healing Herbs (Cemetery) → talk to Aria | Coins, Healers Potion |
| Bread for the Captain | Baker Toma (Bakery) | get Fresh Bread → talk to Captain Elian (Barracks) | Coins |
| Forge Supplies | Borin (Blacksmith) | get Blacksmith Hammer (Warehouse) → talk to Borin | Guard Sword, Coins |
| The Lost Archive | Librarian Ves (Old Library) | get Old Map (Watch Post) + Library Note → talk to Ves | Crypt Key, Coins |
| Smugglers Manifest | Dockmaster Ren (Docks) | defeat Vex → get the Manifest (Vex's loot) → talk to Ren | Warehouse Key, Coins |
| Restore the Beacon | Keeper Ira (Lighthouse) | *requires Smugglers Manifest*; get Crystal Shard → talk to Ira | Mysterious Orb, Coins |

## World Design

**Novaris: The Buried City**, 45 rooms in five areas, every exit two-way
(checked at startup):

```
                                   training_yard  ─ guard_barracks ─   watch_post
                                         │                │                │
                      bakery     ─  merchant_row  ─ market_square  ─ novaris_gate*
                        │                │                │                │
alley_of_whispers ─   res._lane    ─     tavern     ─ fountain_plaza ─   blacksmith
                        │                │                │                │
                   old_library   ─  garden_gate   ─  temple_steps  ─ cemetery_gate  ─    cemetery
                                         │                │                │                │
                                   royal_gardens     temple_hall†  ─  healer_room    abandoned_chapel

  * spawn point   † safe room (respawn), stairs down to the crypts
  hidden_door lies west of alley_of_whispers
```

* **City core** (above): a grid of **many loops**, for example Gate → Watch
  Post → Barracks → Market → Gate, or Market → Merchant Row → Tavern → Fountain
  → Market. Dialogue NPCs and most quest givers live here.
* **Gardens and river:** Royal Gardens → Garden Maze → Maze Shrine → Riverside
  Path → Old Library forms a loop. The river leads to the **Docks**, Warehouse,
  Lighthouse and Lighthouse Road (another loop).
* **Outskirts:** West Gate → Forest Road → Bandit Camp → Ruined Tower →
  Lighthouse is a loop guarded by the Bandit Scout and Bandit Leader Karg. The
  Ruined Tower leads to the cave branch (Cave Entrance ↓ Deep Cave → Crystal
  Cavern).
* **Underground:** Temple Hall ↓ Crypt Entrance → Crypt Hall → Crypt Chamber →
  Sewer Junction → Smugglers' Tunnel → Crypt Hall (loop). The Hidden Door (off
  the Alley of Whispers) and the Warehouse lead down here too. Flooded Sewer →
  Underground Lake → Undercity Ruins → Sewer Junction is the deep loop.
* **Optional branches / dead ends:** Hidden Door, Abandoned Chapel, Crystal
  Cavern, and the **Ancient Vault** (final boss).

The difficulty rises with depth: rats and scouts near the surface, the
Sentinel at the bottom.

**NPC roles** (21 NPCs, 4 roles): *dialogue* (Captain Elian, Recruit Mara,
Innkeeper Sela, Priest Oren, Gravekeeper Nolan, Gardener Elin, The Whisper),
*quest_giver* (Baker Toma, Borin, Healer Aria, Librarian Ves, Dockmaster Ren,
Keeper Ira), *merchant* (Lyra, flavour dialogue) and *enemy* (Giant Sewer Rat,
Bandit Scout, Smuggler Vex, Cave Stalker, Bandit Leader Karg, Ruined Guardian,
Vault Sentinel). The Whisper and the Gravekeeper drop hints about the hidden
routes.

**Items** (25 definitions): weapons and shields scattered along the
combat routes (Training Sword in the Training Yard, Iron Shield at the
blacksmith, Bandit Buckler in the camp). Healing items are near the temple and
bakery, and quest items sit where the quest text says. Some items are only
obtainable as loot (Manifest, Bandit Dagger, Ancient Relic) or as rewards
(Guard Sword, Warehouse Key, Mysterious Orb). *Vault Seal* and *Guard Armor*
are non-obtainable scenery. Every item is a **unique instance**: TAKE moves it
from the room to your inventory, DROP puts it back, and disconnecting drops
everything you carry where you stood.

## Server Logging

**Format.** One JSON object per line (Go `log/slog` JSON handler), with
nanosecond RFC 3339 timestamps, a `level` (`DEBUG`, `INFO`, `WARN`, `ERROR`), a
human `msg` and a machine-friendly `type`:

```json
{"time":"2026-10-20T14:10:33.0729+01:00","level":"INFO","msg":"client connected","type":"connection","ip":"127.0.0.1","remote":"127.0.0.1:53122"}
{"time":"2026-10-20T14:10:33.1012+01:00","level":"INFO","msg":"command","type":"command","player":"alice","ip":"127.0.0.1","command":"TAKE","args":"healing herbs"}
{"time":"2026-10-20T14:10:33.1013+01:00","level":"INFO","msg":"item taken","type":"world","player":"alice","item":"item.healing_herbs","room":"loc.cemetery"}
{"time":"2026-10-20T14:10:33.1014+01:00","level":"INFO","msg":"response","type":"response","player":"alice","ip":"127.0.0.1","command":"TAKE","response":"OK taken=item.healing_herbs"}
{"time":"2026-10-20T14:10:35.5001+01:00","level":"WARN","msg":"error response","type":"response","player":"alice","ip":"127.0.0.1","command":"MOVE","code":"301","response":"ERR 301 NO_EXIT"}
{"time":"2026-10-20T14:10:39.2000+01:00","level":"WARN","msg":"command flood","type":"abuse","player":"mallory","ip":"10.0.0.7","rejected":1}
```

| `type` | Events | Level |
|---|---|---|
| `startup` / `shutdown` | world loaded, listening, signals | INFO / ERROR |
| `world` | world warnings/errors at load; item taken/dropped/used; player moved; NPC respawn | INFO / WARN / ERROR |
| `connection` | connect / disconnect with IP, reason, session duration | INFO |
| `session` | player joined / left (with dropped items) | INFO |
| `command` | every received command: player, IP, command, arguments | INFO |
| `response` | every reply: `OK` at INFO, `ERR` at WARN with its `code` | INFO / WARN |
| `chat`, `group`, `npc` | chat scope + length (not the text), group changes, NPC talk | INFO |
| `combat` | every round (damage, HP, crit, status), defeats, deaths, resets | INFO |
| `quest` | accepted, progress, completed (+ rewards) | INFO |
| `abuse` | command flood, line too long, rapid connections, server full | WARN |
| `network` | send failures / slow clients dropped (code 901) | ERROR |

**Output.** Logs go to **stderr** (`2>server.log` to capture), and also to a
file with `-log-file path` (`make run-server LOG_FILE=server.log`). Use
`-log-level debug` to also see event delivery counts. Records are handed to an
**asynchronous writer** (8192-record buffer drained by a goroutine), so slow
disk or terminal I/O never holds the game lock.

**Abuse detection.**
* *Command flooding:* token bucket per connection (burst 20, refill 8/s).
  Excess commands get `ERR 105 RATE_LIMITED` and a WARN. After 200 rejected
  commands in a row the client is disconnected (`reason":"flood"`).
* *Rapid connections:* more than 10 connections from one IP in 10 s are refused
  with `ERR 900 CONNECTION_FAILED` and a WARN (`reason":"rapid_connections"`).
  Global cap of 128 connections (`server_full`).
* *Oversized lines:* > 1024 bytes → `ERR 104`, WARN.

**Monitoring recipes** (with `jq`):
```sh
./bin/tap-server 2> >(tee server.log)                     # watch live
jq -c 'select(.level != "INFO")' server.log               # problems only
jq -c 'select(.type == "abuse")' server.log               # abuse patterns
jq -r 'select(.type=="command") | .ip' server.log | sort | uniq -c | sort -rn   # busiest IPs
jq -c 'select(.type=="combat" or .type=="quest")' server.log
```

## Group Contributions

> **TODO (team):** fill in with your real split. Each member must also have
> commits in the repository.

| Member | Responsibilities |
|---|---|
| ekrama10 | *e.g.* server core (networking, dispatcher, groups, items), CLI client |
| \<login2\> | *e.g.* GUI client, world design (`world/world.json`) |
| \<login3\> | *e.g.* combat & quest systems, logging, tests |

## Building and Running

The build tool is **GNU make** driving the **Go toolchain** (Go modules,
module name `tap`, no external dependencies).

| Target | Command it runs | Notes |
|---|---|---|
| `make install` | `go version`, `go mod download`, `go mod verify` | fails with a hint if Go is missing |
| `make build` (default) | `go build` of `./server`, `./client`, `./gui` into `bin/` | |
| `make run-server` | `bin/tap-server -addr :$(PORT) -world $(WORLD)` | `PORT=4242`, `WORLD=world/world.json`, `LOG_FILE=` |
| `make run-client` | `bin/tap-client -host $(HOST) -port $(PORT) [-name $(NAME)]` | `HOST=localhost` |
| `make run-client-gui` | `bin/tap-gui -host $(HOST) -port $(PORT) -listen $(GUI_ADDR)` | opens your browser on `http://127.0.0.1:8080` |
| `make check-world` | `bin/tap-server -check` | validates the world file and exits |
| `make lint` | `gofmt -l` (must be empty), `go vet`, `staticcheck` if installed | |
| `make test` | `go test ./...` | |
| `make clean` / `fclean` / `re` | remove `bin/` (+ test cache) / rebuild | |

**Server flags:** `-addr :4242`, `-world world/world.json`,
`-log-file FILE`, `-log-level info`, `-respawn 60s`, `-check`.

**CLI client** (`make run-client NAME=alice`). It uses **approach (2): a
friendly interface that also accepts raw RFC syntax**:
* Any RFC command typed as is is sent unchanged (`MOVE north`, `CHAT GLOBAL hi`).
  Prefix with `/` to bypass shortcuts entirely (`/LOOK`).
* Shortcuts are translated into RFC lines: `n s e w ne nw se sw u d`,
  `go <dir>`, `l`, `i`, `get <item>`, `say` (room), `shout` (global),
  `gsay` (group), `kill <npc>`, `block`, `run`, `eat/drink <item>`, `hp`,
  `journal`, `exit`. Type `help` for the list.
* Replies are pretty-printed (room view, HP bar, combat narration, quest
  lists) by matching each `OK`/`ERR` to the oldest pending command. Events show
  as soon as they arrive, because a dedicated goroutine reads the socket while
  another reads stdin. `-raw` prints the protocol lines verbatim.

**GUI client** (`make run-client-gui`). A Go program serves an embedded single
page (`gui/web/`) on localhost and **bridges** it to the TAP server: one TCP
connection per browser tab. Server lines are streamed with Server-Sent Events,
and commands are POSTed one at a time to keep their order. Cross-origin
requests are refused. We chose web-based (allowed by the subject) because it
needs no C toolchain or system GUI libraries, so it builds anywhere Go does. The
UI has:
* a login form (server host/port + username). `?name=alice&auto=1` logs in
  directly.
* the **room panel**: name, description, compass of exits (click to move),
  **items** with *Take*, **NPCs** with *Talk / Quest / Attack*, players with
  *Invite*.
* an **action bar** with a button for every command (LOOK, MOVE, TAKE, DROP,
  TALK, ATTACK, DEFEND, FLEE, USE, STATUS, QUEST, QUESTS, WHO, INVENTORY,
  GROUP; QUIT sits in the top bar). Commands needing a target use the target field, which accepts an
  **ID or a display name**.
* **inventory** with *Use / Drop*, status/HP bar, quest journal and group panel
  (create, invite, pending invitations with *Join*, leave).
* a **dialogue box** for NPC talk, quest offers and combat results.
* **chat** with Global / Room / Group tabs and unread counters, kept
  **separate from the log view** (all raw traffic, auto refreshes dimmed).
* **counters** for players in the room (from LOOK) and on the server (from
  WHO / `EVT STATS`).
* automatic refreshes: LOOK after MOVE/TAKE/DROP and on presence, item and NPC
  events; INVENTORY after TAKE/DROP/USE/quests; STATUS after combat.

## Testing

**Automated** (`make test`):
* `internal/game`: world requirements, validation errors, name rules,
  LOOK/MOVE/presence events, item uniqueness (ID / name / word lookup, no
  duplication, scenery), chat scopes, the full group lifecycle, TALK/hostility,
  combat to victory with loot and kill credit, death and respawn at the safe
  room, quest delivery/prerequisites/rewards, disconnect cleanup order, and NPC
  respawn.
* `internal/server`: real TCP tests for greeting, auth, **fragmented and
  coalesced commands**, long lines, invalid UTF-8, QUIT, abrupt disconnects
  broadcasting LEAVE, the rate limiter and rapid-connection refusal.

**Manual multiplayer test** (three terminals plus a browser):
```sh
make run-server LOG_FILE=server.log
make run-client NAME=alice
make run-client NAME=bob
make run-client-gui              # log in as carol
nc localhost 4242                # raw protocol, e.g. "CONNECT dave"
```
1. *Presence & chat:* alice `n`. bob sees `alice leaves`, carol's room counter
   drops. `shout hi` reaches everyone, `say hi` only the room.
2. *Items:* alice `get coins` at the gate. bob `get item.novaris_coins` →
   `ITEM_NOT_FOUND`. alice `drop coins`, and the GUI room list updates by
   itself.
3. *Groups:* alice `group create`, `group invite bob`. bob sees the invite and
   types `group join alice`. `gsay hello` reaches only the group. Then
   `group leave`.
4. *Disconnect:* kill bob's terminal (Ctrl-C). Others get `EVT ROOM PRESENCE
   LEAVE bob` and `EVT STATS`, and his items lie on the floor.

**Combat:** from the gate `s`, `s`, `w`, `s` reaches the Temple Hall, then
`d`, `s`, `e`, `s`, `e` reaches the *Flooded Sewer* and its Giant Sewer Rat. `attack rat`, `defend`, `flee`,
`status`. Try the Vault Sentinel unarmed to see death → respawn in the Temple
Hall with 50 HP (`EVT PLAYER RESPAWN`). Kill an enemy and wait 60 s (or start
the server with `-respawn 5s`) to see `EVT ROOM NPC SPAWN`. A second player in
the room sees every round as `EVT ROOM COMBAT`.

**Quests:** Gate `s`, `s` (Cemetery Gate), `e` (Cemetery), `get herbs`, `w`,
`s` (Healer's Room), `quest aria` (accept), `talk aria` → `EVT QUEST COMPLETE`.
`i` shows Coins + Healers Potion and the herbs are gone. `quests` → `completed
2/2`. `quest aria` again → `ERR 406`. `quest ira` at the Lighthouse before
finishing the Smugglers Manifest → `ERR 406` (prerequisite).

**Logs:** `jq -c 'select(.type=="quest" or .type=="combat")' server.log`.
Flood test: `yes WHO | head -500 | nc localhost 4242` → `ERR 105` and an
`abuse` WARN.

## Resources

* RFC 42TAP, *The Answer Protocol* (provided with the subject)
* RFC 2119 (requirement keywords), RFC 5234 (ABNF), RFC 793 (TCP), RFC 3629 (UTF-8)
* Go documentation: [`net`](https://pkg.go.dev/net), [`bufio`](https://pkg.go.dev/bufio),
  [`log/slog`](https://pkg.go.dev/log/slog), [`embed`](https://pkg.go.dev/embed),
  [Effective Go](https://go.dev/doc/effective_go), [Go memory model](https://go.dev/ref/mem)
* MDN: [Server-Sent Events](https://developer.mozilla.org/docs/Web/API/Server-sent_events/Using_server-sent_events)
* Richard Bartle, *Designing Virtual Worlds* (2003), the MUD1 author on
  multi-user world design
* [The MUD Connector](http://www.mudconnect.com/) and the history of DikuMUD / LPMud

**Use of AI.** An AI coding assistant (Claude Code) was used to:
* audit the earlier draft against the subject and RFC, which found broken world
  references, one-way exits, unreachable rooms, the `S:` prefixes on server
  lines and the missing commands;
* help rewrite the server around a dispatcher and outbox, and implement
  combat, quests, logging and the network limits;
* write the CLI pretty-printer, the GUI front-end and bridge, the automated
  tests and a first draft of this README.

Every part was reviewed, tested (`make test`, manual multiplayer sessions) and
discussed within the team, and each member must be able to explain and modify
it during the peer review. **TODO (team):** adjust this paragraph so it
matches exactly how you used AI.
