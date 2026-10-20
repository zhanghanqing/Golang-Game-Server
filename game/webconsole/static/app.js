// Web console controller: wires the page to a GameClient.
import { Codec, shortName } from "./codec.js";
import { GameClient, ServerError } from "./client.js";
import { runSelfTest, stepTitles } from "./selftest.js";

const $ = (id) => document.getElementById(id);

const WORLD = 1000;       // world size (game/gamesvc WorldWidth/Height)
const CELL = 100;         // AOI cell size
const STEP = 20;          // keyboard move step
const MAX_STEP = 45;      // stay under the server's max_move_step (50)

// ------------------------------------------------------------------ i18n
const TEXT = {
  zh: {
    title: "游戏服务器网页控制台", uid: "UID", connTitle: "连接", wsUrl: "网关 WebSocket 地址",
    connect: "连接", disconnect: "断开", loginTitle: "登录与进入游戏", account: "账号", password: "密码",
    charName: "角色名（首次创建时使用）", login: "登录", enter: "进入游戏",
    loginHint: "新账号会自动注册。账号 3–32 位字母数字下划线，密码 6–64 位。",
    selfTestTitle: "一键自检", selfTestHint: "用一个独立连接和全新账号，自动跑完整个客户端流程，逐项检查服务器响应。不影响你当前的会话。",
    runSelfTest: "运行自检", playerTitle: "角色", notInGame: "尚未进入游戏", exp: "经验", gold: "金币", pos: "坐标",
    mapTitle: "场景地图", mapHint: "点击地图或用 WASD / 方向键移动。高亮区域是你的视野（九宫格 AOI）。",
    bagTitle: "背包", refresh: "刷新", itemName: "道具", count: "数量", use: "使用",
    chatTitle: "聊天", world: "世界", scene: "附近", chatPh: "输入消息，回车发送", send: "发送",
    gmTitle: "GM 命令", run: "执行", logTitle: "消息日志", hideHb: "隐藏心跳", clear: "清空",
    st_disconnected: "未连接", st_connected: "已连接", st_loggedin: "已登录", st_ingame: "游戏中",
    nearbyNone: "附近没有其他玩家", kicked: "你被服务器断开：", running: "自检运行中…",
    stPass: (s) => `✔ 全部通过：${s.passed} 项通过，${s.skipped} 项跳过，用时 ${s.ms} ms`,
    stFail: (s) => `✘ 有 ${s.failed} 项失败（通过 ${s.passed}，跳过 ${s.skipped}）`,
    loginOk: (uid, isNew) => `登录成功 uid=${uid}${isNew ? "（新账号）" : ""}`,
    enterOk: "已进入游戏", selftestTag: "自检",
  },
  en: {
    title: "Game Server Web Console", uid: "UID", connTitle: "Connection", wsUrl: "Gate WebSocket URL",
    connect: "Connect", disconnect: "Disconnect", loginTitle: "Login & enter game", account: "Account", password: "Password",
    charName: "Character name (used on first creation)", login: "Log in", enter: "Enter game",
    loginHint: "New accounts are registered automatically. Account: 3–32 letters/digits/_; password: 6–64 chars.",
    selfTestTitle: "One-click self-test", selfTestHint: "Runs the whole client flow on a separate connection with a fresh account and checks every answer. Your own session is not affected.",
    runSelfTest: "Run self-test", playerTitle: "Character", notInGame: "Not in game yet", exp: "EXP", gold: "Gold", pos: "Position",
    mapTitle: "Scene map", mapHint: "Click the map or use WASD / arrow keys to move. The highlighted area is your view (nine-grid AOI).",
    bagTitle: "Bag", refresh: "Refresh", itemName: "Item", count: "Count", use: "Use",
    chatTitle: "Chat", world: "World", scene: "Nearby", chatPh: "Type a message, Enter to send", send: "Send",
    gmTitle: "GM command", run: "Run", logTitle: "Message log", hideHb: "Hide heartbeats", clear: "Clear",
    st_disconnected: "Disconnected", st_connected: "Connected", st_loggedin: "Logged in", st_ingame: "In game",
    nearbyNone: "No other players nearby", kicked: "Disconnected by server: ", running: "Self-test running…",
    stPass: (s) => `✔ All passed: ${s.passed} passed, ${s.skipped} skipped in ${s.ms} ms`,
    stFail: (s) => `✘ ${s.failed} failed (${s.passed} passed, ${s.skipped} skipped)`,
    loginOk: (uid, isNew) => `Logged in, uid=${uid}${isNew ? " (new account)" : ""}`,
    enterOk: "Entered the game", selftestTag: "self-test",
  },
};
let lang = "zh";
try { lang = localStorage.getItem("console.lang") || "zh"; } catch { /* storage unavailable */ }
const t = (key) => TEXT[lang][key] ?? key;

function applyLang() {
  document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
  document.querySelectorAll("[data-i18n]").forEach((el) => { el.textContent = t(el.dataset.i18n); });
  document.querySelectorAll("[data-i18n-ph]").forEach((el) => { el.placeholder = t(el.dataset.i18nPh); });
  $("btnLang").textContent = lang === "zh" ? "EN" : "中文";
  setState(state);
  renderPlayer();
  renderBag();
  renderNearby();
}

// ------------------------------------------------------------------ state
let codec;
let client;
let state = "disconnected"; // disconnected | connected | loggedin | ingame
let uid = null;
let player = null;
let pos = null;
const bag = new Map();      // item_id -> count (string)
const others = new Map();   // uid -> {name, x, y}
let itemNames = new Map();

function setState(s) {
  state = s;
  const pill = $("statusPill");
  pill.textContent = t("st_" + s);
  pill.dataset.state = s;
  pill.className = "pill " + { disconnected: "pill-off", connected: "pill-conn", loggedin: "pill-auth", ingame: "pill-game" }[s];
  const connected = s !== "disconnected";
  $("btnConnect").disabled = connected;
  $("btnDisconnect").disabled = !connected;
  $("btnLogin").disabled = s !== "connected";
  $("btnEnter").disabled = s !== "loggedin";
  for (const id of ["btnBag", "btnChat", "btnGm"]) $(id).disabled = s !== "ingame";
  $("uidText").textContent = uid ?? "—";
}

let toastTimer;
function toast(text, isError = false) {
  const el = $("toast");
  el.textContent = text;
  el.className = "toast" + (isError ? " err" : "");
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, isError ? 5000 : 2500);
}

function describeError(e) {
  return e instanceof ServerError ? `${shortName(e.reqType)} → ${e.codeName} (${e.code}): ${e.serverMessage}` : e.message;
}

async function action(fn) {
  try {
    await fn();
  } catch (e) {
    toast(describeError(e), true);
  }
}

// ------------------------------------------------------------------ log
const LOG_ICON = { send: "→", recv: "←", push: "⇠", info: "•", error: "✕" };

function addLog(d, tag) {
  if ($("logHideHb").checked && d.typeName && d.typeName.startsWith("gs.sys.Heartbeat")) return;
  const li = document.createElement("li");
  if (d.typeName === "gs.sys.ErrorResp" || d.dir === "error") li.classList.add("err");
  const time = new Date().toLocaleTimeString(undefined, { hour12: false });
  const name = d.typeName ? `${shortName(d.typeName)}${d.seq ? " #" + d.seq : ""}` : d.text;
  let body = "";
  if (d.typeName && d.msg) {
    let shown;
    try { shown = codec.display(d.typeName, d.msg); } catch { shown = { ...d.msg }; }
    if (shown && "password" in shown) shown.password = "******"; // never show secrets in the log
    body = JSON.stringify(shown);
  }
  li.innerHTML = `<span class="t"></span><span class="d d-${d.dir}"></span><span class="name"></span>` + (body ? `<span class="body"></span>` : "");
  li.querySelector(".t").textContent = time;
  li.querySelector(".d").textContent = LOG_ICON[d.dir] || "";
  const nameEl = li.querySelector(".name");
  if (tag) {
    const tg = document.createElement("span");
    tg.className = "tag";
    tg.textContent = `[${tag}]`;
    nameEl.append(tg);
  }
  nameEl.append(name);
  if (body) li.querySelector(".body").textContent = body;
  li.addEventListener("click", () => li.classList.toggle("open"));
  const list = $("logList");
  list.prepend(li);
  while (list.children.length > 400) list.lastChild.remove();
}

// ------------------------------------------------------------------ render
function renderPlayer() {
  if (!player) {
    $("playerCard").hidden = true;
    $("playerEmpty").hidden = false;
    return;
  }
  $("playerEmpty").hidden = true;
  $("playerCard").hidden = false;
  $("pName").textContent = player.name;
  $("pLevel").textContent = "Lv." + player.level;
  const need = player.level * 100; // game/logic/levelup.ExpToNext
  $("pExp").textContent = `${player.exp} / ${need}`;
  $("pExpBar").style.width = Math.min(100, (Number(player.exp) / need) * 100) + "%";
  $("pGold").textContent = player.gold;
  $("pPos").textContent = pos ? `(${pos.x.toFixed(1)}, ${pos.y.toFixed(1)})` : "—";
}

function renderBag() {
  const body = $("bagBody");
  body.innerHTML = "";
  if (state !== "ingame") {
    body.innerHTML = `<tr><td colspan="4" class="empty"></td></tr>`;
    body.querySelector("td").textContent = t("notInGame");
    return;
  }
  const ids = [...bag.keys()].sort((a, b) => a - b);
  for (const id of ids) {
    const tr = document.createElement("tr");
    tr.innerHTML = "<td></td><td></td><td></td><td></td>";
    tr.children[0].textContent = id;
    tr.children[1].textContent = itemNames.get(id)?.name ?? "?";
    tr.children[2].textContent = bag.get(id);
    if (itemNames.get(id)?.usable) {
      const btn = document.createElement("button");
      btn.className = "ghost small";
      btn.textContent = t("use");
      btn.dataset.item = id;
      btn.addEventListener("click", () => action(() => useItem(id)));
      tr.children[3].append(btn);
    }
    body.append(tr);
  }
}

function renderNearby() {
  const el = $("nearby");
  el.innerHTML = "";
  if (others.size === 0) {
    const s = document.createElement("span");
    s.textContent = t("nearbyNone");
    el.append(s);
  }
  for (const [id, o] of others) {
    const s = document.createElement("span");
    s.textContent = `${o.name} (${Math.round(o.x)}, ${Math.round(o.y)})`;
    s.title = "uid " + id;
    el.append(s);
  }
  drawMap();
}

function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }

function drawMap() {
  const cv = $("map");
  const dpr = window.devicePixelRatio || 1;
  const size = cv.clientWidth || 320;
  if (cv.width !== size * dpr) { cv.width = size * dpr; cv.height = size * dpr; }
  const g = cv.getContext("2d");
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  const k = size / WORLD;
  g.fillStyle = cssVar("--map-bg");
  g.fillRect(0, 0, size, size);
  if (pos) { // AOI: 3x3 cells around the player
    const cx = Math.floor(pos.x / CELL), cy = Math.floor(pos.y / CELL);
    g.fillStyle = cssVar("--map-aoi");
    g.fillRect((cx - 1) * CELL * k, (cy - 1) * CELL * k, 3 * CELL * k, 3 * CELL * k);
  }
  g.strokeStyle = cssVar("--map-grid");
  g.lineWidth = 1;
  for (let i = 0; i <= WORLD; i += CELL) {
    g.beginPath(); g.moveTo(i * k + 0.5, 0); g.lineTo(i * k + 0.5, size); g.stroke();
    g.beginPath(); g.moveTo(0, i * k + 0.5); g.lineTo(size, i * k + 0.5); g.stroke();
  }
  g.font = "11px system-ui, sans-serif";
  for (const o of others.values()) {
    g.fillStyle = cssVar("--map-other");
    g.beginPath(); g.arc(o.x * k, o.y * k, 4, 0, Math.PI * 2); g.fill();
    g.fillStyle = cssVar("--muted");
    g.fillText(o.name, o.x * k + 6, o.y * k - 4);
  }
  if (pos) {
    g.fillStyle = cssVar("--accent");
    g.beginPath(); g.arc(pos.x * k, pos.y * k, 5.5, 0, Math.PI * 2); g.fill();
    g.fillStyle = cssVar("--text");
    g.fillText(player?.name ?? "me", pos.x * k + 7, pos.y * k + 14);
  }
}

function addChat(m) {
  const list = $("chatList");
  const div = document.createElement("div");
  div.className = "msg";
  div.innerHTML = `<span class="ch"></span><span class="who"></span><span class="txt"></span>`;
  div.querySelector(".ch").textContent = m.channel === 2 ? `[${t("scene")}]` : `[${t("world")}]`;
  div.querySelector(".who").textContent = m.from_name + ":";
  div.querySelector(".txt").textContent = m.text;
  list.append(div);
  list.scrollTop = list.scrollHeight;
}

// ------------------------------------------------------------------ pushes
function onPush({ typeName, msg }) {
  switch (typeName) {
    case "gs.game.PlayerInfoPush":
      player = msg.player;
      renderPlayer();
      break;
    case "gs.game.BagUpdatePush":
      for (const it of msg.items) {
        if (it.count === "0") bag.delete(it.item_id); else bag.set(it.item_id, it.count);
      }
      renderBag();
      break;
    case "gs.game.ChatPush":
      addChat(msg);
      break;
    case "gs.game.SceneEntityEnterPush":
      for (const e of msg.entities) if (e.uid !== uid) others.set(e.uid, { name: e.name, x: e.position.x, y: e.position.y });
      renderNearby();
      break;
    case "gs.game.SceneEntityLeavePush":
      for (const id of msg.uids) others.delete(id);
      renderNearby();
      break;
    case "gs.game.SceneEntityMovePush": {
      const o = others.get(msg.uid);
      if (o) { o.x = msg.position.x; o.y = msg.position.y; renderNearby(); }
      break;
    }
  }
}

function resetSession() {
  uid = null; player = null; pos = null;
  bag.clear(); others.clear();
  renderPlayer(); renderBag(); renderNearby();
}

// ------------------------------------------------------------------ actions
async function connect() {
  $("banner").hidden = true;
  await client.connect($("wsUrl").value.trim());
  setState("connected");
  await client.heartbeat();
  updateRtt();
  client.startHeartbeat(10000);
}

async function login() {
  const r = await client.request("gs.game.LoginReq", {
    account: $("account").value.trim(), password: $("password").value, client_version: "web-console",
  });
  uid = r.uid;
  setState("loggedin");
  toast(TEXT[lang].loginOk(r.uid, r.new_account));
}

async function enterGame() {
  const r = await client.request("gs.game.EnterGameReq", { name: $("charName").value.trim() });
  player = r.player;
  pos = { x: r.position.x, y: r.position.y };
  setState("ingame");
  renderPlayer();
  await refreshBag();
  toast(t("enterOk"));
}

async function refreshBag() {
  const r = await client.request("gs.game.BagGetReq");
  bag.clear();
  for (const it of r.items) bag.set(it.item_id, it.count);
  renderBag();
}

async function useItem(id) {
  await client.request("gs.game.ItemUseReq", { item_id: id, count: "1" });
}

async function sendChat() {
  const text = $("chatText").value.trim();
  if (!text) return;
  await client.request("gs.game.ChatSendReq", { channel: $("chatChannel").value, text });
  $("chatText").value = "";
}

async function runGm() {
  const r = await client.request("gs.game.GmCommandReq", { command: $("gmCmd").value.trim() });
  $("gmOut").hidden = false;
  $("gmOut").textContent = r.output;
}

let moving = false;
async function moveTo(x, y) {
  if (state !== "ingame" || !pos || moving) return;
  // Clamp to the world and to a step the server accepts.
  let dx = x - pos.x, dy = y - pos.y;
  const dist = Math.hypot(dx, dy);
  if (dist > MAX_STEP) { dx *= MAX_STEP / dist; dy *= MAX_STEP / dist; }
  const target = {
    x: Math.round(Math.min(WORLD - 1, Math.max(0, pos.x + dx)) * 10) / 10,
    y: Math.round(Math.min(WORLD - 1, Math.max(0, pos.y + dy)) * 10) / 10,
  };
  moving = true;
  try {
    const r = await client.request("gs.game.SceneMoveReq", { target });
    pos = { x: r.position.x, y: r.position.y };
    renderPlayer();
    drawMap();
  } finally {
    moving = false;
  }
}

function updateRtt() { $("rttText").textContent = client.rtt == null ? "—" : client.rtt + " ms"; }

// ------------------------------------------------------------------ self-test
async function selfTest() {
  const btn = $("btnSelfTest");
  btn.disabled = true;
  document.body.dataset.selftest = "running";
  const list = $("selftestList");
  list.innerHTML = "";
  const rows = stepTitles().map((title) => {
    const li = document.createElement("li");
    li.className = "st-pending";
    li.innerHTML = `<span class="ico">○</span><span class="title"></span><span class="ms"></span><span class="detail"></span>`;
    li.querySelector(".title").textContent = title;
    list.append(li);
    return li;
  });
  const sum = $("selftestSummary");
  sum.hidden = false;
  sum.className = "summary";
  sum.textContent = t("running");

  const icons = { running: "…", pass: "✔", fail: "✘", skip: "–" };
  const summary = await runSelfTest(codec, $("wsUrl").value.trim(), (i, r) => {
    const li = rows[i];
    li.className = "st-" + r.status;
    li.dataset.status = r.status;
    li.querySelector(".ico").textContent = icons[r.status];
    li.querySelector(".ms").textContent = r.ms != null ? r.ms + " ms" : "";
    li.querySelector(".detail").textContent = r.detail ? String(r.detail) : "";
  }, (d) => addLog(d, t("selftestTag")));

  sum.className = "summary " + (summary.ok ? "ok" : "fail");
  sum.textContent = summary.ok ? TEXT[lang].stPass(summary) : TEXT[lang].stFail(summary);
  window.__selftest = summary; // read by the automated browser test
  document.body.dataset.selftest = summary.ok ? "pass" : "fail";
  btn.disabled = false;
}

// ------------------------------------------------------------------ boot
async function main() {
  const schema = await (await fetch("schema.json", { cache: "no-store" })).json();
  codec = new Codec(schema);
  itemNames = new Map(schema.items.map((i) => [i.id, i]));
  client = new GameClient(codec);
  client.addEventListener("log", (e) => addLog(e.detail));
  client.addEventListener("push", (e) => onPush(e.detail));
  client.addEventListener("close", (e) => {
    if (e.detail.kick) {
      const b = $("banner");
      b.textContent = t("kicked") + e.detail.kick;
      b.hidden = false;
    }
    setState("disconnected"); // first: the views below render according to the state
    resetSession();
    updateRtt();
  });
  setInterval(updateRtt, 2000);

  const scheme = location.protocol === "https:" ? "wss" : "ws";
  $("wsUrl").value = `${scheme}://${location.hostname}:${schema.ws_port}${schema.ws_path}`;
  const suffix = Math.random().toString(36).slice(2, 6);
  $("account").value = "player_" + suffix;
  $("password").value = "123456";
  $("charName").value = "Hero_" + suffix;

  $("btnConnect").addEventListener("click", () => action(connect));
  $("btnDisconnect").addEventListener("click", () => client.close());
  $("btnLogin").addEventListener("click", () => action(login));
  $("btnEnter").addEventListener("click", () => action(enterGame));
  $("btnBag").addEventListener("click", () => action(refreshBag));
  $("btnChat").addEventListener("click", () => action(sendChat));
  $("chatText").addEventListener("keydown", (e) => { if (e.key === "Enter") action(sendChat); });
  $("btnGm").addEventListener("click", () => action(runGm));
  $("gmCmd").addEventListener("keydown", (e) => { if (e.key === "Enter") action(runGm); });
  $("btnSelfTest").addEventListener("click", () => selfTest());
  $("btnClearLog").addEventListener("click", () => { $("logList").innerHTML = ""; });
  $("btnLang").addEventListener("click", () => {
    lang = lang === "zh" ? "en" : "zh";
    try { localStorage.setItem("console.lang", lang); } catch { /* ignore */ }
    applyLang();
  });

  $("map").addEventListener("click", (e) => {
    if (!pos) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const k = WORLD / rect.width;
    action(() => moveTo((e.clientX - rect.left) * k, (e.clientY - rect.top) * k));
  });
  const keys = { ArrowUp: [0, -1], ArrowDown: [0, 1], ArrowLeft: [-1, 0], ArrowRight: [1, 0], w: [0, -1], s: [0, 1], a: [-1, 0], d: [1, 0] };
  document.addEventListener("keydown", (e) => {
    const tag = document.activeElement?.tagName;
    if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
    const v = keys[e.key] || keys[e.key.toLowerCase()];
    if (!v || !pos) return;
    e.preventDefault();
    action(() => moveTo(pos.x + v[0] * STEP, pos.y + v[1] * STEP));
  });
  window.addEventListener("resize", drawMap);

  applyLang();
  document.body.dataset.ready = "1";
}

main().catch((e) => {
  document.body.dataset.ready = "error";
  toast("Failed to start console: " + e.message, true);
});
