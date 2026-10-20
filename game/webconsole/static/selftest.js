// One-click self-test: plays the whole client flow on a dedicated connection
// with a fresh account and checks every answer. It does not touch the session
// the user is driving manually.

import { GameClient, ServerError } from "./client.js";

class StepFailed extends Error {}

function check(cond, msg) {
  if (!cond) throw new StepFailed(msg);
}

async function expectError(promise, codeName) {
  try {
    await promise;
  } catch (e) {
    if (e instanceof ServerError) {
      check(e.codeName === codeName, `expected ${codeName}, got ${e.codeName}`);
      return `server answered ${e.codeName} as expected`;
    }
    throw e;
  }
  throw new StepFailed(`expected error ${codeName}, but the request succeeded`);
}

const near = (a, b) => Math.abs(a - b) < 0.01;

// Each step: {title, critical?, run(ctx) -> detail string | {skip: string}}
const STEPS = [
  {
    title: "连接网关 / Connect to gate (WebSocket)",
    critical: true,
    run: async (ctx) => {
      await ctx.c.connect(ctx.url);
      return ctx.url;
    },
  },
  {
    title: "心跳 / Heartbeat",
    run: async (ctx) => {
      const r = await ctx.c.heartbeat();
      check(BigInt(r.server_time_ms) > 0n, "server_time_ms missing");
      return `RTT ${ctx.c.rtt} ms`;
    },
  },
  {
    title: "未登录请求被拒绝 / Unauthenticated request rejected",
    run: (ctx) => expectError(ctx.c.request("gs.game.BagGetReq"), "NOT_AUTHENTICATED"),
  },
  {
    title: "登录（自动注册）/ Login (auto-register)",
    critical: true,
    run: async (ctx) => {
      const r = await ctx.c.request("gs.game.LoginReq", {
        account: ctx.account, password: "selftest123", client_version: "web-console",
      });
      check(r.uid !== "0", "uid is 0");
      check(r.new_account === true, "expected a new account");
      ctx.uid = r.uid;
      return `account ${ctx.account}, uid ${r.uid}`;
    },
  },
  {
    title: "进入游戏 / Enter game",
    critical: true,
    run: async (ctx) => {
      const r = await ctx.c.request("gs.game.EnterGameReq", { name: ctx.account });
      check(r.player && r.player.uid === ctx.uid, "player uid mismatch");
      check(r.player.level === 1, `level ${r.player.level}, want 1`);
      ctx.player = r.player;
      ctx.pos = { x: r.position.x, y: r.position.y };
      return `${r.player.name} Lv.${r.player.level} at (${ctx.pos.x}, ${ctx.pos.y})`;
    },
  },
  {
    title: "查看背包 / Get bag",
    run: async (ctx) => {
      const r = await ctx.c.request("gs.game.BagGetReq");
      check(r.items.length === 3, `starter bag has ${r.items.length} stacks, want 3`);
      const pouch = r.items.find((i) => i.item_id === 1002);
      check(pouch && pouch.count === "2", "expected 2 × item 1002 (Gold Pouch)");
      return r.items.map((i) => `${i.item_id}×${i.count}`).join(", ");
    },
  },
  {
    title: "使用道具（金币袋 +100）/ Use item",
    run: async (ctx) => {
      const since = ctx.c.mark();
      const goldBefore = BigInt(ctx.player.gold);
      await ctx.c.request("gs.game.ItemUseReq", { item_id: 1002, count: "1" });
      const info = await ctx.c.waitPush("gs.game.PlayerInfoPush", () => true, 3000, since);
      check(BigInt(info.player.gold) === goldBefore + 100n, `gold ${info.player.gold}, want ${goldBefore + 100n}`);
      const bag = await ctx.c.waitPush("gs.game.BagUpdatePush", () => true, 3000, since);
      check(bag.items[0].item_id === 1002 && bag.items[0].count === "1", "bag update push mismatch");
      return `gold ${goldBefore} → ${info.player.gold}, pushes received`;
    },
  },
  {
    title: "错误处理：不可用道具 / Error: unusable item",
    run: (ctx) => expectError(ctx.c.request("gs.game.ItemUseReq", { item_id: 2001, count: "1" }), "ERR_ITEM_NOT_USABLE"),
  },
  {
    title: "世界聊天 / World chat",
    run: async (ctx) => {
      const text = `self-test ${Date.now()}`;
      const since = ctx.c.mark();
      await ctx.c.request("gs.game.ChatSendReq", { channel: "CHAT_CHANNEL_WORLD", text });
      const push = await ctx.c.waitPush("gs.game.ChatPush", (m) => m.text === text, 3000, since);
      check(push.from_uid === ctx.uid, "chat sender mismatch");
      return "broadcast received back";
    },
  },
  {
    title: "场景移动 / Move in scene",
    run: async (ctx) => {
      const target = { x: ctx.pos.x + 10, y: ctx.pos.y + 5 };
      const r = await ctx.c.request("gs.game.SceneMoveReq", { target });
      check(near(r.position.x, target.x) && near(r.position.y, target.y), "position mismatch");
      ctx.pos = target;
      return `now at (${target.x}, ${target.y})`;
    },
  },
  {
    title: "防作弊：瞬移被拒绝 / Anti-cheat: teleport rejected",
    run: (ctx) => expectError(
      ctx.c.request("gs.game.SceneMoveReq", { target: { x: ctx.pos.x + 300, y: ctx.pos.y } }),
      "ERR_MOVE_TOO_FAR"),
  },
  {
    title: "GM 命令 / GM command",
    run: async (ctx) => {
      try {
        const r = await ctx.c.request("gs.game.GmCommandReq", { command: "info" });
        check(r.output.includes(ctx.uid), "GM output does not mention uid");
        return r.output;
      } catch (e) {
        if (e instanceof ServerError && e.codeName === "ERR_GM_DISABLED") {
          return { skip: "GM disabled on this server (expected in production)" };
        }
        throw e;
      }
    },
  },
  {
    title: "断开连接 / Disconnect",
    run: async (ctx) => {
      const closed = new Promise((resolve) => ctx.c.addEventListener("close", resolve, { once: true }));
      ctx.c.ws.close();
      await closed;
      return "closed cleanly";
    },
  },
];

export const stepTitles = () => STEPS.map((s) => s.title);

// runSelfTest(codec, url, onUpdate) -> summary. onUpdate(index, result) is called
// when a step starts ("running") and when it ends ("pass" | "fail" | "skip").
export async function runSelfTest(codec, url, onUpdate, onLog) {
  const c = new GameClient(codec);
  if (onLog) c.addEventListener("log", (e) => onLog(e.detail));
  const ctx = { c, url, account: "selftest_" + Math.random().toString(36).slice(2, 10) };
  const results = [];
  const started = performance.now();
  let abort = false;

  for (let i = 0; i < STEPS.length; i++) {
    const step = STEPS[i];
    if (abort) {
      results[i] = { title: step.title, status: "skip", detail: "skipped: an earlier critical step failed" };
      onUpdate(i, results[i]);
      continue;
    }
    onUpdate(i, { title: step.title, status: "running" });
    const t0 = performance.now();
    try {
      const out = await step.run(ctx);
      const ms = Math.round(performance.now() - t0);
      results[i] = out && out.skip
        ? { title: step.title, status: "skip", detail: out.skip, ms }
        : { title: step.title, status: "pass", detail: out, ms };
    } catch (e) {
      results[i] = { title: step.title, status: "fail", detail: e.message, ms: Math.round(performance.now() - t0) };
      if (step.critical) abort = true;
    }
    onUpdate(i, results[i]);
  }
  c.close();

  const count = (s) => results.filter((r) => r.status === s).length;
  return {
    passed: count("pass"),
    failed: count("fail"),
    skipped: count("skip"),
    total: results.length,
    ms: Math.round(performance.now() - started),
    ok: count("fail") === 0,
    results,
  };
}
