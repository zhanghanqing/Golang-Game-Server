// GameClient: one WebSocket connection to a gate.
//
// It mirrors framework/client (the Go SDK): requests carry a non-zero seq and
// are matched with their response; messages with seq 0 are server pushes.
// An ErrorResp answer rejects the request promise with a ServerError.
//
// Events (EventTarget):
//   "log"   detail {dir: "send"|"recv"|"push"|"info"|"error", typeName, seq, msg, text}
//   "push"  detail {typeName, msg}
//   "close" detail {code, reason, kick}

export class ServerError extends Error {
  constructor(code, name, message, reqType) {
    super(`${name} (${code}): ${message}`);
    this.code = code;
    this.codeName = name;
    this.serverMessage = message;
    this.reqType = reqType;
  }
}

export class GameClient extends EventTarget {
  constructor(codec) {
    super();
    this.codec = codec;
    this.ws = null;
    this.seq = 0;
    this.pending = new Map(); // seq -> {resolve, reject, timer, typeName}
    this.pushes = [];         // recent pushes, for waitPush
    this.pushCount = 0;
    this.kick = null;
    this.hbTimer = null;
    this.rtt = null;
  }

  get connected() { return this.ws !== null && this.ws.readyState === WebSocket.OPEN; }

  log(detail) { this.dispatchEvent(new CustomEvent("log", { detail })); }

  connect(url, timeoutMs = 5000) {
    this.close();
    this.kick = null;
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      this.ws = ws;
      const timer = setTimeout(() => { ws.close(); reject(new Error("connect timeout")); }, timeoutMs);
      ws.onopen = () => {
        clearTimeout(timer);
        this.log({ dir: "info", text: `connected to ${url}` });
        resolve();
      };
      ws.onerror = () => {
        clearTimeout(timer);
        reject(new Error(`cannot connect to ${url}`));
      };
      ws.onmessage = (ev) => this.onMessage(ev.data);
      ws.onclose = (ev) => {
        clearTimeout(timer);
        if (this.ws === ws) this.ws = null;
        this.stopHeartbeat();
        for (const [, p] of this.pending) { clearTimeout(p.timer); p.reject(new Error("connection closed")); }
        this.pending.clear();
        this.log({ dir: "info", text: `disconnected (code ${ev.code})` + (this.kick ? ` — kicked: ${this.kick}` : "") });
        this.dispatchEvent(new CustomEvent("close", { detail: { code: ev.code, reason: ev.reason, kick: this.kick } }));
      };
    });
  }

  close() {
    this.stopHeartbeat();
    if (this.ws) {
      const ws = this.ws;
      this.ws = null;
      ws.close();
    }
  }

  request(typeName, obj = {}, timeoutMs = 8000) {
    if (!this.connected) return Promise.reject(new Error("not connected"));
    this.seq = (this.seq % 0xffffffff) + 1; // never 0: 0 means "push"
    const seq = this.seq;
    const data = this.codec.packet(typeName, seq, obj);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(seq);
        reject(new Error(`${typeName} timed out`));
      }, timeoutMs);
      this.pending.set(seq, { resolve, reject, timer, typeName });
      this.ws.send(data);
      this.log({ dir: "send", typeName, seq, msg: obj });
    });
  }

  onMessage(data) {
    let pkt;
    try {
      pkt = this.codec.parsePacket(data);
    } catch (e) {
      this.log({ dir: "error", text: "bad packet: " + e.message });
      return;
    }
    if (!pkt.typeName) {
      this.log({ dir: "error", text: `unknown msg_id ${pkt.id} (ignored)` });
      return;
    }
    if (pkt.seq !== 0) {
      const p = this.pending.get(pkt.seq);
      this.log({ dir: "recv", typeName: pkt.typeName, seq: pkt.seq, msg: pkt.msg });
      if (!p) return;
      this.pending.delete(pkt.seq);
      clearTimeout(p.timer);
      if (pkt.typeName === "gs.sys.ErrorResp") {
        const name = this.codec.errorNames.get(pkt.msg.code) || "ERROR";
        p.reject(new ServerError(pkt.msg.code, name, pkt.msg.message, p.typeName));
      } else {
        p.resolve(pkt.msg);
      }
      return;
    }
    if (pkt.typeName === "gs.sys.KickPush") {
      this.kick = this.codec.enumName("gs.sys.KickReason", pkt.msg.reason);
    }
    this.pushes.push({ n: ++this.pushCount, typeName: pkt.typeName, msg: pkt.msg });
    if (this.pushes.length > 500) this.pushes.shift();
    this.log({ dir: "push", typeName: pkt.typeName, seq: 0, msg: pkt.msg });
    this.dispatchEvent(new CustomEvent("push", { detail: { typeName: pkt.typeName, msg: pkt.msg } }));
  }

  // mark() returns a cursor; waitPush(..., since) only matches pushes after it.
  mark() { return this.pushCount; }

  waitPush(typeName, pred = () => true, timeoutMs = 3000, since = 0) {
    const find = () => this.pushes.find((p) => p.n > since && p.typeName === typeName && pred(p.msg));
    const hit = find();
    if (hit) return Promise.resolve(hit.msg);
    return new Promise((resolve, reject) => {
      const onPush = () => {
        const h = find();
        if (h) { cleanup(); resolve(h.msg); }
      };
      const timer = setTimeout(() => { cleanup(); reject(new Error(`no ${typeName} push within ${timeoutMs} ms`)); }, timeoutMs);
      const cleanup = () => { clearTimeout(timer); this.removeEventListener("push", onPush); };
      this.addEventListener("push", onPush);
    });
  }

  async heartbeat() {
    const sent = Date.now();
    const resp = await this.request("gs.sys.HeartbeatReq", { client_time_ms: String(sent) });
    this.rtt = Date.now() - sent;
    return resp;
  }

  startHeartbeat(intervalMs = 10000) {
    this.stopHeartbeat();
    this.hbTimer = setInterval(() => { this.heartbeat().catch(() => {}); }, intervalMs);
  }

  stopHeartbeat() {
    if (this.hbTimer) clearInterval(this.hbTimer);
    this.hbTimer = null;
  }
}
