// Command client is the TAP command-line client.
//
// It accepts raw RFC 42TAP commands ("MOVE north") as well as friendlier
// shortcuts ("n", "say hi", "get herbs") that it translates into protocol
// lines. Server messages are read on their own goroutine so events are shown
// while the user is typing.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

type pending struct {
	cmd  string
	args string
}

type ui struct {
	conn     net.Conn
	raw      bool
	color    bool
	queue    []pending
	quitting bool
}

func main() {
	host := flag.String("host", "localhost", "server host")
	port := flag.Int("port", 4242, "server port")
	name := flag.String("name", "", "connect automatically with this username")
	raw := flag.Bool("raw", false, "print server lines verbatim")
	flag.Parse()

	addr := net.JoinHostPort(*host, fmt.Sprint(*port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to", addr+":", err)
		os.Exit(1)
	}
	defer conn.Close()

	st, _ := os.Stdout.Stat()
	u := &ui{conn: conn, raw: *raw, color: st != nil && st.Mode()&os.ModeCharDevice != 0}

	serverLines := make(chan string)
	go func() {
		defer close(serverLines)
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			serverLines <- strings.TrimRight(sc.Text(), "\r")
		}
	}()

	userLines := make(chan string)
	go func() {
		defer close(userLines)
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			userLines <- sc.Text()
		}
	}()

	u.info("Connected to " + addr + ". Type 'help' for commands.")
	if *name != "" {
		u.send("CONNECT " + *name)
	}
	u.prompt()

	for {
		select {
		case line, ok := <-serverLines:
			if !ok {
				u.info("Connection closed by server.")
				return
			}
			u.show(line)
			if u.quitting && line == "OK bye" {
				return
			}
			u.prompt()
		case in, ok := <-userLines:
			if !ok {
				u.send("QUIT")
				u.quitting = true
				userLines = nil
				go func() { time.Sleep(2 * time.Second); conn.Close() }()
				continue
			}
			u.handleInput(in)
			u.prompt()
		}
	}
}

// ---------------------------------------------------------------------------
// Output helpers

const (
	cReset  = "\033[0m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cBlue   = "\033[34m"
	cCyan   = "\033[36m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
)

func (u *ui) paint(code, s string) string {
	if !u.color {
		return s
	}
	return code + s + cReset
}

func (u *ui) println(s string) {
	if u.color {
		fmt.Print("\r\033[K")
	}
	fmt.Println(s)
}

func (u *ui) info(s string) { u.println(u.paint(cDim, s)) }

func (u *ui) prompt() {
	if u.color {
		fmt.Print(u.paint(cBold, "> "))
	}
}

func (u *ui) send(line string) {
	cmd, args, _ := strings.Cut(strings.TrimSpace(line), " ")
	cmd = strings.ToUpper(cmd)
	if _, err := fmt.Fprintf(u.conn, "%s\n", strings.TrimSpace(cmd+" "+args)); err != nil {
		u.println(u.paint(cRed, "send failed: "+err.Error()))
		return
	}
	u.queue = append(u.queue, pending{cmd: cmd, args: strings.TrimSpace(args)})
	if cmd == "QUIT" {
		u.quitting = true
	}
}

// ---------------------------------------------------------------------------
// Input translation

var directions = map[string]string{
	"n": "north", "s": "south", "e": "east", "w": "west", "u": "up", "d": "down",
	"ne": "northeast", "nw": "northwest", "se": "southeast", "sw": "southwest",
	"north": "north", "south": "south", "east": "east", "west": "west", "up": "up", "down": "down",
	"northeast": "northeast", "northwest": "northwest", "southeast": "southeast", "southwest": "southwest",
}

var aliases = map[string]string{
	"l": "LOOK", "look": "LOOK",
	"go": "MOVE", "move": "MOVE", "walk": "MOVE",
	"say": "CHAT ROOM", "shout": "CHAT GLOBAL", "yell": "CHAT GLOBAL",
	"gsay": "CHAT GROUP", "party": "CHAT GROUP",
	"get": "TAKE", "take": "TAKE", "pick": "TAKE",
	"drop": "DROP",
	"i":    "INVENTORY", "inv": "INVENTORY", "inventory": "INVENTORY",
	"talk": "TALK", "speak": "TALK",
	"attack": "ATTACK", "kill": "ATTACK", "hit": "ATTACK", "fight": "ATTACK",
	"defend": "DEFEND", "block": "DEFEND",
	"flee": "FLEE", "run": "FLEE",
	"use": "USE", "eat": "USE", "drink": "USE",
	"status": "STATUS", "hp": "STATUS",
	"quest": "QUEST", "quests": "QUESTS", "journal": "QUESTS",
	"who": "WHO", "group": "GROUP",
	"quit": "QUIT", "exit": "QUIT", "q": "QUIT",
	"connect": "CONNECT", "login": "CONNECT",
}

func (u *ui) handleInput(in string) {
	in = strings.TrimSpace(in)
	if in == "" {
		return
	}
	if strings.HasPrefix(in, "/") { // raw protocol line, sent untouched
		u.send(in[1:])
		return
	}
	word, rest, _ := strings.Cut(in, " ")
	rest = strings.TrimSpace(rest)
	lw := strings.ToLower(word)
	switch {
	case lw == "help" || lw == "?":
		u.help()
	case directions[lw] != "" && rest == "":
		u.send("MOVE " + directions[lw])
	case aliases[lw] != "":
		u.send(strings.TrimSpace(aliases[lw] + " " + rest))
	default:
		u.send(in) // unknown word: forward as an RFC command
	}
}

func (u *ui) help() {
	lines := []string{
		"Movement   : n s e w ne nw se sw u d | go <dir> | MOVE <dir>",
		"Looking    : look (l) | who | status (hp) | inventory (i)",
		"Items      : get <item> | drop <item> | use <item>   (ID or display name)",
		"NPCs       : talk <npc> | quest <npc> | quests",
		"Combat     : attack <npc> | defend | flee | use <item>",
		"Chat       : say <msg> (room) | shout <msg> (global) | gsay <msg> (group)",
		"Groups     : group create | group invite <name> | group join <leader> | group leave",
		"Session    : connect <name> | quit",
		"Raw        : any RFC command is sent as typed; prefix with / to bypass shortcuts",
	}
	for _, l := range lines {
		u.info(l)
	}
}

// ---------------------------------------------------------------------------
// Server message rendering

func (u *ui) show(line string) {
	if u.raw {
		u.println(line)
		return
	}
	switch {
	case strings.HasPrefix(line, "EVT "):
		u.println(u.event(line))
	case strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "ERR"):
		if strings.HasPrefix(line, "OK hello") && len(u.queue) == 0 {
			u.info("Server: " + line)
			return
		}
		var p pending
		if len(u.queue) > 0 {
			p, u.queue = u.queue[0], u.queue[1:]
		}
		if strings.HasPrefix(line, "ERR") {
			u.println(u.paint(cRed, "✗ "+strings.TrimPrefix(line, "ERR ")))
			return
		}
		u.println(u.reply(p, strings.TrimSpace(strings.TrimPrefix(line, "OK"))))
	default:
		u.println(line)
	}
}

func pretty(id string) string {
	if i := strings.IndexByte(id, '.'); i >= 0 {
		id = id[i+1:]
	}
	words := strings.Fields(strings.ReplaceAll(id, "_", " "))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func (u *ui) list(ids []string) string {
	if len(ids) == 0 {
		return u.paint(cDim, "none")
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		if strings.Contains(id, ".") {
			out[i] = pretty(id) + u.paint(cDim, " ["+id+"]")
		} else {
			out[i] = id
		}
	}
	return strings.Join(out, ", ")
}

func (u *ui) reply(p pending, data string) string {
	ok := u.paint(cGreen, "✓ ")
	var obj map[string]any
	isObj := json.Unmarshal([]byte(data), &obj) == nil

	switch p.cmd {
	case "LOOK":
		var l struct {
			Room struct {
				ID, Name, Description string
				Exits                 map[string]string
			} `json:"room"`
			Players []string `json:"players"`
			Items   []string `json:"items"`
			NPCs    []string `json:"npcs"`
		}
		if json.Unmarshal([]byte(data), &l) != nil {
			break
		}
		dirs := make([]string, 0, len(l.Room.Exits))
		for d := range l.Room.Exits {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		return strings.Join([]string{
			u.paint(cBold+cCyan, "== "+l.Room.Name+" ==") + u.paint(cDim, " ("+l.Room.ID+")"),
			l.Room.Description,
			u.paint(cYellow, "Exits:   ") + strings.Join(dirs, ", "),
			u.paint(cYellow, "Items:   ") + u.list(l.Items),
			u.paint(cYellow, "NPCs:    ") + u.list(l.NPCs),
			u.paint(cYellow, "Players: ") + strings.Join(l.Players, ", "),
		}, "\n")
	case "INVENTORY":
		var items []string
		if json.Unmarshal([]byte(data), &items) == nil {
			return ok + "Inventory: " + u.list(items)
		}
	case "TALK":
		if isObj { // other servers may answer {"npc":..., "dialogue":...}
			return u.paint(cCyan, fmt.Sprintf("%v: ", obj["npc"])) + fmt.Sprint(obj["dialogue"])
		}
		return u.paint(cCyan, pretty(p.args)+": ") + "\"" + data + "\""
	case "STATUS":
		if isObj {
			hp, _ := obj["hp"].(float64)
			maxHP, _ := obj["max_hp"].(float64)
			s := fmt.Sprintf("HP %.0f/%.0f  [%s]  %v", hp, maxHP, bar(hp, maxHP), obj["status"])
			if t, ok := obj["target"].(string); ok && t != "" {
				s += fmt.Sprintf("  fighting %s (%v HP)", pretty(t), obj["target_hp"])
			}
			return ok + s
		}
	case "ATTACK", "DEFEND", "FLEE", "USE":
		if isObj {
			return ok + u.combat(obj)
		}
	case "QUEST":
		if isObj {
			s := fmt.Sprintf("Quest %v [%v %v]: %v", obj["name"], obj["status"], obj["progress"], obj["description"])
			if objs, ok := obj["objectives"].([]any); ok {
				for _, o := range objs {
					s += "\n   - " + fmt.Sprint(o)
				}
			}
			if r, ok := obj["reward"]; ok {
				s += "\n   Reward: " + fmt.Sprint(r)
			}
			return ok + s
		}
	case "QUESTS":
		var qs []map[string]any
		if json.Unmarshal([]byte(data), &qs) == nil {
			if len(qs) == 0 {
				return ok + "No quests yet."
			}
			s := "Quests:"
			for _, q := range qs {
				s += fmt.Sprintf("\n   - %v (%v) %v %v", q["name"], q["quest_id"], q["status"], q["progress"])
			}
			return ok + s
		}
	case "WHO":
		if isObj { // tolerate {"room":[...],"server":N}
			return ok + fmt.Sprintf("In room: %v | on server: %v", obj["room"], obj["server"])
		}
		return ok + strings.Replace(data, "players=", "Players online: ", 1)
	}
	if data == "" {
		return ok + strings.ToLower(p.cmd) + " done"
	}
	return ok + data
}

func bar(v, maxV float64) string {
	if maxV <= 0 {
		return ""
	}
	n := int(v / maxV * 20)
	return strings.Repeat("#", n) + strings.Repeat(".", 20-n)
}

func (u *ui) combat(o map[string]any) string {
	var b strings.Builder
	switch o["action"] {
	case "use":
		fmt.Fprintf(&b, "You use %s and recover %v HP.", pretty(fmt.Sprint(o["item"])), num(o["healed"]))
	case "defend":
		b.WriteString("You raise your guard.")
	case "flee":
		if o["status"] == "fled" {
			fmt.Fprintf(&b, "You escape to %s!", pretty(fmt.Sprint(o["room"])))
		} else {
			b.WriteString("You fail to escape!")
		}
	default:
		crit := ""
		if o["critical"] == true {
			crit = " CRITICAL!"
		}
		fmt.Fprintf(&b, "You hit %s for %v damage.%s", pretty(fmt.Sprint(o["target"])), num(o["damage"]), crit)
	}
	if t, ok := o["target"]; ok && t != "" && o["status"] != "fled" {
		fmt.Fprintf(&b, " Enemy HP %v/%v.", num(o["target_hp"]), num(o["target_max_hp"]))
	}
	if c := num(o["counter_damage"]); c != "" && c != "0" {
		fmt.Fprintf(&b, " It strikes back for %s.", c)
	}
	fmt.Fprintf(&b, " Your HP %v.", num(o["attacker_hp"]))
	switch o["status"] {
	case "victory":
		b.WriteString(u.paint(cGreen, " Victory!"))
		if loot, ok := o["loot"].([]any); ok && len(loot) > 0 {
			fmt.Fprintf(&b, " Loot dropped: %v", loot)
		}
	case "defeated":
		b.WriteString(u.paint(cRed, " You were defeated and wake up in "+pretty(fmt.Sprint(o["room"]))+"."))
	}
	return b.String()
}

func num(v any) string {
	if f, ok := v.(float64); ok {
		return fmt.Sprintf("%.0f", f)
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (u *ui) event(line string) string {
	f := strings.Fields(line)
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	// rest(i) is the line from field i onwards, spacing preserved.
	rest := func(i int) string {
		parts := strings.SplitN(line, " ", i+1)
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	switch get(1) + " " + get(2) {
	case "GLOBAL CHAT":
		return u.paint(cBlue, "[global] "+get(3)+": ") + rest(4)
	case "ROOM CHAT":
		return u.paint(cCyan, "[room] "+get(3)+": ") + rest(4)
	case "GROUP CHAT":
		return u.paint(cGreen, "[group] "+get(3)+": ") + rest(4)
	case "ROOM PRESENCE":
		if get(3) == "ENTER" {
			return u.paint(cYellow, "* "+get(4)+" arrives.")
		}
		return u.paint(cYellow, "* "+get(4)+" leaves.")
	}
	switch {
	case get(1) == "STATS":
		return u.paint(cDim, "* "+strings.Replace(rest(2), "players=", "players online: ", 1))
	case get(1) == "GROUP" && get(2) == "INVITE":
		return u.paint(cGreen, "* "+get(3)+" invites you to their group. Type: group join "+get(3))
	case get(1) == "GROUP" && get(2) == "JOIN":
		return u.paint(cGreen, "* "+get(3)+" joined the group.")
	case get(1) == "GROUP" && get(2) == "LEAVE":
		return u.paint(cGreen, "* "+get(3)+" left the group.")
	case get(1) == "QUEST":
		var q map[string]any
		if json.Unmarshal([]byte(rest(3)), &q) == nil {
			if get(2) == "COMPLETE" {
				return u.paint(cGreen+cBold, fmt.Sprintf("* Quest completed: %v! Rewards: %v", q["name"], q["rewards"]))
			}
			return u.paint(cGreen, fmt.Sprintf("* Quest %v progress: %v", q["name"], q["progress"]))
		}
	case get(1) == "ROOM" && get(2) == "COMBAT":
		var c map[string]any
		if json.Unmarshal([]byte(rest(3)), &c) == nil {
			return u.paint(cRed, fmt.Sprintf("* %v %v %s (%v): %v dmg, enemy HP %v, status %v",
				c["attacker"], c["action"], pretty(fmt.Sprint(c["target"])), c["target"], num(c["damage"]), num(c["target_hp"]), c["status"]))
		}
	case get(1) == "ROOM" && get(2) == "ITEM":
		return u.paint(cYellow, fmt.Sprintf("* %s %s %s.", get(4), strings.ToLower(get(3)), pretty(get(5))))
	case get(1) == "ROOM" && get(2) == "NPC":
		if get(3) == "DEFEATED" {
			return u.paint(cYellow, fmt.Sprintf("* %s was defeated by %s.", pretty(get(4)), get(5)))
		}
		return u.paint(cYellow, fmt.Sprintf("* %s appears.", pretty(get(4))))
	case get(1) == "COMBAT" && get(2) == "HIT":
		var c map[string]any
		if json.Unmarshal([]byte(rest(3)), &c) == nil {
			return u.paint(cRed, fmt.Sprintf("* %s strikes you as you leave: %v damage, HP %v.", pretty(fmt.Sprint(c["npc"])), num(c["damage"]), num(c["hp"])))
		}
	case get(1) == "PLAYER" && get(2) == "RESPAWN":
		return u.paint(cRed, "* You respawn: "+rest(3))
	}
	return u.paint(cDim, line)
}
