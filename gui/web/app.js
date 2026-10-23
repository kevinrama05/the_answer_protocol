"use strict";
// TAP GUI front-end. It speaks plain RFC 42TAP through the local Go bridge:
// server lines arrive on an EventSource, commands are POSTed one at a time.
// Replies carry no command name, so every sent command is queued and each
// OK/ERR line is matched with the oldest pending command.

const $ = (id) => document.getElementById(id);

const state = {
  sid: null,
  es: null,
  me: "",
  connected: false,
  gotHello: false,
  queue: [],                  // pending commands: {cmd, args, auto}
  chain: Promise.resolve(),   // serialises POSTs so order is preserved
  room: null,
  inventory: [],
  hp: null, maxHp: 100,
  group: null,                // {id, members:Set}
  invites: new Set(),
  tab: "GLOBAL",
  chats: { GLOBAL: [], ROOM: [], GROUP: [] },
  unread: { GLOBAL: 0, ROOM: 0, GROUP: 0 },
};

// ---------------------------------------------------------------- helpers

function el(tag, attrs = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") n.className = v;
    else if (k === "onclick") n.addEventListener("click", v);
    else n.setAttribute(k, v);
  }
  for (const c of children) {
    if (c == null) continue;
    n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return n;
}

// "item.healing_herbs" -> "Healing Herbs"
function pretty(id) {
  const s = String(id);
  const base = s.includes(".") ? s.slice(s.indexOf(".") + 1) : s;
  return base.split(/[_\s]+/).filter(Boolean)
    .map((w) => w[0].toUpperCase() + w.slice(1)).join(" ");
}

function parseJSON(s) {
  try { return JSON.parse(s); } catch { return undefined; }
}

function now() {
  return new Date().toLocaleTimeString([], { hour12: false });
}

// ---------------------------------------------------------------- log

function log(text, kind = "", auto = false) {
  const box = $("log");
  const line = el("div", { class: `log-line ${kind}${auto ? " auto" : ""}` },
    el("span", { class: "time" }, now()), text);
  if (auto && !$("show-auto").checked) line.classList.add("hidden");
  const stick = box.scrollTop + box.clientHeight >= box.scrollHeight - 20;
  box.append(line);
  while (box.childElementCount > 1500) box.firstChild.remove();
  if (stick) box.scrollTop = box.scrollHeight;
}

$("show-auto").addEventListener("change", (e) => {
  for (const n of document.querySelectorAll(".log-line.auto")) n.classList.toggle("hidden", !e.target.checked);
});

// ---------------------------------------------------------------- network

function connect(host, port, name) {
  $("login-error").textContent = "";
  const qs = new URLSearchParams({ host, port });
  const es = new EventSource("/events?" + qs);
  state.es = es;
  state.gotHello = false;
  state.queue = [];
  es.addEventListener("session", (e) => {
    state.sid = e.data;
    log(`Connected to ${host}:${port}`, "sys");
    send("CONNECT " + name);
  });
  es.addEventListener("failure", (e) => {
    $("login-error").textContent = "Cannot reach server: " + e.data;
    es.close();
  });
  es.addEventListener("closed", () => disconnected("The server closed the connection."));
  es.onmessage = (e) => onLine(e.data);
  es.onerror = () => {
    if (es.readyState === EventSource.CLOSED || state.sid) disconnected("Connection to the GUI bridge lost.");
  };
}

function disconnected(msg) {
  if (state.es) state.es.close();
  state.es = null;
  state.sid = null;
  state.connected = false;
  state.queue = [];
  log(msg, "sys");
  $("game").classList.add("hidden");
  $("login").classList.remove("hidden");
  $("login-error").textContent = msg;
}

function send(line, auto = false) {
  line = line.trim();
  if (!line || !state.sid) return;
  const sp = line.indexOf(" ");
  const cmd = (sp < 0 ? line : line.slice(0, sp)).toUpperCase();
  const args = sp < 0 ? "" : line.slice(sp + 1).trim();
  const entry = { cmd, args, auto };
  state.queue.push(entry);
  log("> " + line, "out", auto);
  const sid = state.sid;
  state.chain = state.chain.then(() =>
    fetch("/send?sid=" + encodeURIComponent(sid), { method: "POST", body: line })
      .then((r) => { if (!r.ok) throw new Error(r.status + " " + r.statusText); })
      .catch((err) => {
        const i = state.queue.indexOf(entry);
        if (i >= 0) state.queue.splice(i, 1);
        log("send failed: " + err.message, "err");
      }));
}

// Automatic refresh, skipped when the same command is already pending.
function refresh(...cmds) {
  for (const c of cmds) {
    if (!state.queue.some((p) => p.cmd === c && p.args === "")) send(c, true);
  }
}

// ---------------------------------------------------------------- dispatch

function onLine(line) {
  if (line.startsWith("EVT ")) {
    log(line, "evt");
    onEvent(line);
    return;
  }
  if (line.startsWith("OK") || line.startsWith("ERR")) {
    if (!state.gotHello && line.startsWith("OK hello")) {
      state.gotHello = true;
      log(line, "sys");
      return;
    }
    const p = state.queue.shift() || { cmd: "?", args: "", auto: false };
    const err = line.startsWith("ERR");
    log(line, err ? "err" : "", p.auto);
    onReply(p, line, err);
    return;
  }
  log(line);
}

function onReply(p, line, err) {
  const data = line.replace(/^OK ?/, "");
  if (err) {
    if (p.cmd === "CONNECT") {
      $("login-error").textContent = line;
      if (state.es) state.es.close();
      state.sid = null;
    } else if (!p.auto) {
      flash(line);
    }
    return;
  }
  switch (p.cmd) {
    case "CONNECT":
      state.connected = true;
      state.me = p.args;
      $("who-me").textContent = state.me;
      $("login").classList.add("hidden");
      $("game").classList.remove("hidden");
      refresh("LOOK", "INVENTORY", "STATUS", "WHO", "QUESTS");
      break;
    case "LOOK": renderRoom(parseJSON(data)); break;
    case "MOVE": hideDialogue(); refresh("LOOK"); break;
    case "TAKE":
    case "DROP": refresh("LOOK", "INVENTORY"); break;
    case "INVENTORY": renderInventory(parseJSON(data)); break;
    case "TALK": showTalk(p.args, data); break;
    case "ATTACK": case "DEFEND": case "FLEE": case "USE": onCombat(p.cmd, parseJSON(data)); break;
    case "STATUS": renderStatus(parseJSON(data)); break;
    case "QUEST": showQuest(parseJSON(data)); refresh("QUESTS", "INVENTORY"); break;
    case "QUESTS": renderQuests(parseJSON(data)); break;
    case "WHO": onWho(data); break;
    case "GROUP": onGroupReply(p.args, data); break;
    case "QUIT": disconnected("You left the game."); break;
  }
}

function onEvent(line) {
  const f = line.split(" ");
  const rest = (i) => f.slice(i).join(" ");
  const kind = f[1] + " " + f[2];
  switch (kind) {
    case "GLOBAL CHAT": addChat("GLOBAL", f[3], rest(4)); return;
    case "ROOM CHAT": addChat("ROOM", f[3], rest(4)); return;
    case "GROUP CHAT": addChat("GROUP", f[3], rest(4)); return;
    case "ROOM PRESENCE":
      addChat("ROOM", null, `${f[4]} ${f[3] === "ENTER" ? "arrives" : "leaves"}.`);
      refresh("LOOK");
      return;
    case "GROUP INVITE":
      state.invites.add(f[3]);
      renderGroup();
      addChat("GROUP", null, `${f[3]} invited you to their group.`);
      return;
    case "GROUP JOIN":
      if (state.group) state.group.members.add(f[3]);
      addChat("GROUP", null, `${f[3]} joined the group.`);
      renderGroup();
      return;
    case "GROUP LEAVE":
      if (state.group) state.group.members.delete(f[3]);
      addChat("GROUP", null, `${f[3]} left the group.`);
      renderGroup();
      return;
    case "ROOM ITEM":
    case "ROOM NPC":
      refresh("LOOK");
      return;
    case "ROOM COMBAT": {
      const c = parseJSON(rest(3));
      if (c) addChat("ROOM", null, `${c.attacker} ${c.action}s ${pretty(c.target)}: ${c.damage} dmg (enemy ${c.target_hp} HP, ${c.status}).`);
      return;
    }
    case "QUEST PROGRESS":
    case "QUEST COMPLETE": {
      const q = parseJSON(rest(3));
      if (q && f[2] === "COMPLETE") showDialogue("Quest complete", `${q.name}: rewards ${(q.rewards || []).map(pretty).join(", ")}`);
      refresh("QUESTS", "INVENTORY");
      return;
    }
    case "PLAYER RESPAWN":
      showDialogue("Defeated", "You wake up in " + pretty(rest(3).split(" ")[0].replace("room=", "")) + ".");
      refresh("STATUS", "LOOK");
      return;
    case "COMBAT HIT": refresh("STATUS"); return;
  }
  if (f[1] === "STATS") {
    const m = /players=(\d+)/.exec(line);
    if (m) $("count-server").textContent = m[1];
  }
}

// ---------------------------------------------------------------- rendering

function emptyItem(text) {
  return el("li", { class: "empty" }, text);
}

function mini(label, cmd, cls = "") {
  return el("button", { class: "mini " + cls, onclick: () => send(cmd) }, label);
}

const COMPASS = ["northwest", "north", "northeast", "west", null, "east", "southwest", "south", "southeast"];
const SHORT = { northwest: "NW", north: "N", northeast: "NE", west: "W", east: "E", southwest: "SW", south: "S", southeast: "SE" };

function renderRoom(look) {
  if (!look || !look.room) return;
  state.room = look;
  const r = look.room;
  $("room-name").textContent = r.name || pretty(r.id);
  $("room-id").textContent = r.id || "";
  $("room-desc").textContent = r.description || "";

  const exits = r.exits || {};
  const compass = $("exits");
  compass.replaceChildren();
  for (const d of COMPASS) {
    if (!d) { compass.append(el("div", { class: "center" }, "◎")); continue; }
    const b = el("button", { title: exits[d] ? pretty(exits[d]) : "", onclick: () => send("MOVE " + d) }, SHORT[d]);
    b.disabled = !exits[d];
    compass.append(b);
  }
  const extra = $("exits-extra");
  extra.replaceChildren();
  for (const d of Object.keys(exits).filter((d) => !COMPASS.includes(d))) {
    extra.append(el("button", { title: pretty(exits[d]), onclick: () => send("MOVE " + d) }, d));
  }

  const items = $("room-items");
  items.replaceChildren();
  for (const id of look.items || []) {
    items.append(el("li", {},
      el("span", { class: "name", title: id }, pretty(id), " ", el("span", { class: "id" }, id)),
      mini("Take", "TAKE " + id)));
  }
  if (!items.childElementCount) items.append(emptyItem("Nothing here."));

  const npcs = $("room-npcs");
  npcs.replaceChildren();
  for (const id of look.npcs || []) {
    npcs.append(el("li", {},
      el("span", { class: "name", title: id }, pretty(id)),
      mini("Talk", "TALK " + id), mini("Quest", "QUEST " + id), mini("Attack", "ATTACK " + id, "danger")));
  }
  if (!npcs.childElementCount) npcs.append(emptyItem("Nobody."));

  const players = $("room-players");
  players.replaceChildren();
  for (const name of look.players || []) {
    const li = el("li", {}, el("span", { class: "name" }, name === state.me ? name + " (you)" : name));
    if (name !== state.me && state.group) li.append(mini("Invite", "GROUP INVITE " + name));
    players.append(li);
  }
  $("count-room").textContent = (look.players || []).length;
}

function renderInventory(items) {
  if (!Array.isArray(items)) return;
  state.inventory = items;
  const ul = $("inventory");
  ul.replaceChildren();
  for (const id of items) {
    ul.append(el("li", {},
      el("span", { class: "name", title: id }, pretty(id), " ", el("span", { class: "id" }, id)),
      mini("Use", "USE " + id), mini("Drop", "DROP " + id)));
  }
  if (!items.length) ul.append(emptyItem("Empty."));
}

function setHP(hp, max) {
  if (typeof hp !== "number") return;
  state.hp = hp;
  if (typeof max === "number") state.maxHp = max;
  const pct = Math.max(0, Math.min(100, (hp / state.maxHp) * 100));
  $("hp-fill").style.width = pct + "%";
  $("hp-fill").style.background = pct > 60 ? "var(--accent)" : pct > 25 ? "var(--accent-2)" : "var(--danger)";
  $("hp-text").textContent = `${hp}/${state.maxHp}`;
}

function renderStatus(s) {
  if (!s) return;
  setHP(s.hp, s.max_hp);
  $("hp-status").textContent = s.status || "–";
  const lines = [`HP ${s.hp}/${s.max_hp} — ${s.status}`];
  if (s.target) lines.push(`Fighting ${pretty(s.target)} (${s.target_hp} HP)`);
  if (s.room) lines.push(`In ${pretty(s.room)}`);
  $("status").replaceChildren(...lines.map((l) => el("div", {}, l)));
}

function onCombat(cmd, c) {
  if (!c) return;
  setHP(c.attacker_hp);
  let text;
  if (c.action === "use") text = `You use ${pretty(c.item)} and recover ${c.healed || 0} HP.`;
  else if (c.action === "defend") text = `You defend. ${pretty(c.target)} hits for ${c.counter_damage}.`;
  else if (c.action === "flee") text = c.status === "fled" ? `You flee to ${pretty(c.room)}!` : `You fail to flee and take ${c.counter_damage}.`;
  else text = `You hit ${pretty(c.target)} for ${c.damage}${c.critical ? " (critical!)" : ""}. It strikes back for ${c.counter_damage || 0}.`;
  if (c.target && c.status !== "fled") text += ` Enemy HP ${c.target_hp}/${c.target_max_hp}.`;
  if (c.status === "victory") text += " Victory!" + (c.loot && c.loot.length ? " Loot: " + c.loot.map(pretty).join(", ") : "");
  if (c.status === "defeated") text += " You were defeated…";
  showDialogue("Combat", text);
  refresh("STATUS");
  if (cmd === "USE") refresh("INVENTORY");
  if (["victory", "fled", "defeated"].includes(c.status)) refresh("LOOK");
}

function renderQuests(list) {
  if (!Array.isArray(list)) return;
  const ul = $("quests");
  ul.replaceChildren();
  for (const q of list) {
    ul.append(el("li", {},
      el("span", { class: "name", title: q.quest_id }, q.name || pretty(q.quest_id)),
      el("span", { class: "tag" + (q.status === "completed" ? " done" : "") }, q.status + (q.progress ? " " + q.progress : ""))));
  }
  if (!list.length) ul.append(emptyItem("Ask NPCs with QUEST."));
}

function showQuest(q) {
  if (!q) return;
  const obj = (q.objectives || []).map((o) => "• " + o).join("\n");
  showDialogue(`Quest: ${q.name || pretty(q.quest_id)} (${q.status} ${q.progress || ""})`,
    `${q.description || ""}\n${obj}\nReward: ${(q.rewards || [q.reward]).filter(Boolean).map(pretty).join(", ")}`);
}

function showTalk(target, data) {
  const j = parseJSON(data); // tolerate {"npc":..., "dialogue":...}
  if (j && typeof j === "object") showDialogue(pretty(j.npc || target), j.dialogue || data);
  else showDialogue(pretty(target), data);
}

function showDialogue(who, text) {
  $("dialogue").classList.remove("hidden");
  $("dialogue-npc").textContent = who;
  $("dialogue-text").textContent = text;
  $("dialogue-text").style.whiteSpace = "pre-line";
}

function hideDialogue() {
  $("dialogue").classList.add("hidden");
}

function flash(msg) {
  showDialogue("Server", msg);
}

function onWho(data) {
  const m = /players=(\d+)/.exec(data);
  if (m) { $("count-server").textContent = m[1]; return; }
  const j = parseJSON(data); // tolerate {"room":[...],"server":N}
  if (j) {
    if (typeof j.server === "number") $("count-server").textContent = j.server;
    if (Array.isArray(j.room)) $("count-room").textContent = j.room.length;
  }
}

function onGroupReply(args, data) {
  const sub = args.split(" ")[0].toUpperCase();
  const m = /group=(\S+)/.exec(data);
  if (sub === "CREATE" && m) state.group = { id: m[1], members: new Set([state.me]) };
  if (sub === "JOIN" && m) {
    const leader = args.split(" ")[1] || "";
    state.group = { id: m[1], members: new Set([leader, state.me].filter(Boolean)) };
    state.invites.delete(leader);
  }
  if (sub === "LEAVE") state.group = null;
  renderGroup();
  if (state.room) renderRoom(state.room);
}

function renderGroup() {
  const g = state.group;
  $("group-info").textContent = g ? `Group ${g.id}` : "Not in a group.";
  const ul = $("group-members");
  ul.replaceChildren();
  if (g) for (const m of g.members) ul.append(el("li", {}, el("span", { class: "name" }, m)));
  const inv = $("invites");
  inv.replaceChildren();
  for (const leader of state.invites) {
    inv.append(el("li", {}, el("span", { class: "name" }, `Invite from ${leader}`), mini("Join", "GROUP JOIN " + leader)));
  }
}

// ---------------------------------------------------------------- chat

function addChat(scope, who, text) {
  state.chats[scope].push({ t: now(), who, text });
  if (state.chats[scope].length > 500) state.chats[scope].shift();
  if (scope === state.tab) renderChat();
  else { state.unread[scope]++; renderTabs(); }
}

function renderTabs() {
  for (const b of document.querySelectorAll("#chat-tabs button")) {
    const t = b.dataset.tab;
    b.classList.toggle("active", t === state.tab);
    b.querySelector(".unread").textContent = state.unread[t] ? `(${state.unread[t]})` : "";
  }
  $("chat-input").placeholder = "Message to " + state.tab[0] + state.tab.slice(1).toLowerCase();
}

function renderChat() {
  const box = $("chat-log");
  box.replaceChildren(...state.chats[state.tab].map((m) =>
    el("div", { class: "msg" }, el("span", { class: "time" }, m.t),
      m.who ? el("span", { class: "who" }, m.who + ": ") : null,
      m.who ? m.text : el("em", { class: "muted" }, m.text))));
  box.scrollTop = box.scrollHeight;
}

document.querySelectorAll("#chat-tabs button").forEach((b) => b.addEventListener("click", () => {
  state.tab = b.dataset.tab;
  state.unread[state.tab] = 0;
  renderTabs();
  renderChat();
}));

$("chat-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const msg = $("chat-input").value.trim();
  if (!msg) return;
  send(`CHAT ${state.tab} ${msg}`);
  $("chat-input").value = "";
});

// ---------------------------------------------------------------- controls

document.querySelectorAll("button[data-cmd]").forEach((b) => b.addEventListener("click", () => {
  const cmd = b.dataset.cmd;
  const target = $("target").value.trim();
  if (b.dataset.needs && !target) {
    flash(`${cmd} needs a ${b.dataset.needs}: type it in the target field (ID or display name).`);
    $("target").focus();
    return;
  }
  send(b.dataset.needs ? `${cmd} ${target}` : cmd);
}));

$("target").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && $("target").value.trim()) send($("target").value);
});

$("raw-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const v = $("raw-input").value.trim();
  if (v) send(v);
  $("raw-input").value = "";
});

$("btn-quit").addEventListener("click", () => send("QUIT"));
$("g-create").addEventListener("click", () => send("GROUP CREATE"));
$("g-leave").addEventListener("click", () => send("GROUP LEAVE"));
$("g-invite-btn").addEventListener("click", () => {
  const n = $("g-invite").value.trim();
  if (n) send("GROUP INVITE " + n);
  $("g-invite").value = "";
});

$("login-form").addEventListener("submit", (e) => {
  e.preventDefault();
  if (state.es) state.es.close();
  try { localStorage.setItem("tap-name", $("in-name").value.trim()); } catch { /* storage unavailable */ }
  connect($("in-host").value.trim(), $("in-port").value.trim(), $("in-name").value.trim());
});

// Defaults come from the bridge flags; ?host=&port=&name=&auto=1 override
// them and can log in directly.
const params = new URLSearchParams(location.search);
try { $("in-name").value = localStorage.getItem("tap-name") || ""; } catch { /* storage unavailable */ }
if (params.get("name")) $("in-name").value = params.get("name");
fetch("/config").then((r) => r.json()).catch(() => ({ host: "localhost", port: "4242" })).then((c) => {
  $("in-host").value = params.get("host") || c.host;
  $("in-port").value = params.get("port") || c.port;
  if (params.get("auto") === "1" && $("in-name").value) connect($("in-host").value, $("in-port").value, $("in-name").value);
});
renderTabs();
