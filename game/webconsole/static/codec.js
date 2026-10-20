// Minimal proto3 binary codec driven by /console/schema.json.
//
// Supports every field kind used by the protocol: varint kinds (int32, int64,
// uint32, uint64, sint32, sint64, bool, enum), fixed 32/64-bit kinds (float,
// double, fixed32, sfixed32, fixed64, sfixed64), string, bytes, nested
// messages, repeated fields (packed and unpacked).
//
// 64-bit integers are represented as decimal strings so no precision is lost
// (uids are Snowflake ids larger than 2^53).

const VARINT = 0, FIXED64 = 1, LEN = 2, FIXED32 = 5;

const VARINT_KINDS = new Set(["int32", "int64", "uint32", "uint64", "sint32", "sint64", "bool", "enum"]);
const FIXED32_KINDS = new Set(["float", "fixed32", "sfixed32"]);
const FIXED64_KINDS = new Set(["double", "fixed64", "sfixed64"]);
const INT64_KINDS = new Set(["int64", "uint64", "sint64", "fixed64", "sfixed64"]);

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

function wireType(kind) {
  if (VARINT_KINDS.has(kind)) return VARINT;
  if (FIXED32_KINDS.has(kind)) return FIXED32;
  if (FIXED64_KINDS.has(kind)) return FIXED64;
  return LEN;
}

function isPackable(kind) {
  return VARINT_KINDS.has(kind) || FIXED32_KINDS.has(kind) || FIXED64_KINDS.has(kind);
}

class Writer {
  constructor() { this.buf = []; }
  varint(v) {
    v = BigInt.asUintN(64, BigInt(v));
    while (v >= 0x80n) {
      this.buf.push(Number(v & 0x7fn) | 0x80);
      v >>= 7n;
    }
    this.buf.push(Number(v));
  }
  tag(number, wt) { this.varint((BigInt(number) << 3n) | BigInt(wt)); }
  raw(bytes) { for (const b of bytes) this.buf.push(b); }
  lenPrefixed(bytes) { this.varint(bytes.length); this.raw(bytes); }
  fixed(bytesLE) { this.raw(bytesLE); }
  finish() { return Uint8Array.from(this.buf); }
}

class Reader {
  constructor(bytes) { this.b = bytes; this.pos = 0; }
  eof() { return this.pos >= this.b.length; }
  varint() {
    let result = 0n, shift = 0n;
    for (;;) {
      if (this.pos >= this.b.length) throw new Error("truncated varint");
      const byte = this.b[this.pos++];
      result |= BigInt(byte & 0x7f) << shift;
      if ((byte & 0x80) === 0) return result;
      shift += 7n;
      if (shift > 70n) throw new Error("varint too long");
    }
  }
  take(n) {
    if (this.pos + n > this.b.length) throw new Error("truncated field");
    const out = this.b.subarray(this.pos, this.pos + n);
    this.pos += n;
    return out;
  }
  skip(wt) {
    switch (wt) {
      case VARINT: this.varint(); break;
      case FIXED64: this.take(8); break;
      case LEN: this.take(Number(this.varint())); break;
      case FIXED32: this.take(4); break;
      default: throw new Error("unsupported wire type " + wt);
    }
  }
}

export class Codec {
  constructor(schema) {
    this.schema = schema;
    this.nameById = new Map();
    this.idByName = new Map();
    for (const [id, name] of Object.entries(schema.ids)) {
      this.nameById.set(Number(id), name);
      this.idByName.set(name, Number(id));
    }
    // code -> NAME for both framework and game error enums.
    this.errorNames = new Map();
    for (const enumName of ["gs.sys.ErrorCode", "gs.game.ErrCode"]) {
      for (const [k, v] of Object.entries(schema.enums[enumName] || {})) {
        if (!this.errorNames.has(v)) this.errorNames.set(v, k);
      }
    }
  }

  fields(typeName) {
    const f = this.schema.messages[typeName];
    if (!f) throw new Error("unknown message type " + typeName);
    return f;
  }

  enumValue(enumType, v) {
    if (typeof v === "string" && !/^-?\d+$/.test(v)) {
      const n = this.schema.enums[enumType]?.[v];
      if (n === undefined) throw new Error(`unknown ${enumType} value ${v}`);
      return n;
    }
    return Number(v);
  }

  enumName(enumType, n) {
    for (const [k, v] of Object.entries(this.schema.enums[enumType] || {})) if (v === n) return k;
    return String(n);
  }

  // ---------------------------------------------------------------- encode

  encode(typeName, obj) {
    const w = new Writer();
    obj = obj || {};
    for (const f of this.fields(typeName)) {
      const v = obj[f.name];
      if (v === undefined || v === null) continue;
      if (f.repeated) {
        if (!Array.isArray(v) || v.length === 0) continue;
        if (isPackable(f.kind)) {
          const inner = new Writer();
          for (const item of v) this.writeScalar(inner, f, item);
          w.tag(f.number, LEN);
          w.lenPrefixed(inner.finish());
        } else {
          for (const item of v) this.writeField(w, f, item);
        }
      } else if (!this.isDefault(f, v)) {
        this.writeField(w, f, v);
      }
    }
    return w.finish();
  }

  isDefault(f, v) {
    switch (f.kind) {
      case "message": return false; // a present sub-message is always written
      case "string": return v === "";
      case "bytes": return v.length === 0;
      case "bool": return v === false;
      case "enum": return this.enumValue(f.type, v) === 0;
      default:
        if (VARINT_KINDS.has(f.kind) || INT64_KINDS.has(f.kind)) return BigInt(v) === 0n;
        return Number(v) === 0;
    }
  }

  writeField(w, f, v) {
    if (f.kind === "message") {
      w.tag(f.number, LEN);
      w.lenPrefixed(this.encode(f.type, v));
    } else if (f.kind === "string") {
      w.tag(f.number, LEN);
      w.lenPrefixed(textEncoder.encode(v));
    } else if (f.kind === "bytes") {
      w.tag(f.number, LEN);
      w.lenPrefixed(v);
    } else {
      w.tag(f.number, wireType(f.kind));
      this.writeScalar(w, f, v);
    }
  }

  writeScalar(w, f, v) {
    const dv8 = new DataView(new ArrayBuffer(8));
    const bytes = (n) => new Uint8Array(dv8.buffer, 0, n);
    switch (f.kind) {
      case "bool": w.varint(v ? 1 : 0); break;
      case "enum": w.varint(BigInt(this.enumValue(f.type, v))); break;
      case "int32": case "int64": case "uint32": case "uint64": w.varint(BigInt(v)); break;
      case "sint32": case "sint64": {
        const n = BigInt(v);
        w.varint(n >= 0n ? n << 1n : ((-n) << 1n) - 1n);
        break;
      }
      case "float": dv8.setFloat32(0, Number(v), true); w.fixed(bytes(4)); break;
      case "fixed32": dv8.setUint32(0, Number(v), true); w.fixed(bytes(4)); break;
      case "sfixed32": dv8.setInt32(0, Number(v), true); w.fixed(bytes(4)); break;
      case "double": dv8.setFloat64(0, Number(v), true); w.fixed(bytes(8)); break;
      case "fixed64": dv8.setBigUint64(0, BigInt(v), true); w.fixed(bytes(8)); break;
      case "sfixed64": dv8.setBigInt64(0, BigInt(v), true); w.fixed(bytes(8)); break;
      default: throw new Error("cannot write scalar kind " + f.kind);
    }
  }

  // ---------------------------------------------------------------- decode

  decode(typeName, bytes) {
    const fields = this.fields(typeName);
    const byNumber = new Map(fields.map((f) => [f.number, f]));
    const out = {};
    const r = new Reader(bytes);
    while (!r.eof()) {
      const key = r.varint();
      const number = Number(key >> 3n);
      const wt = Number(key & 7n);
      const f = byNumber.get(number);
      if (!f) { r.skip(wt); continue; } // unknown field: forward compatible
      if (f.repeated && wt === LEN && isPackable(f.kind)) {
        const sub = new Reader(r.take(Number(r.varint())));
        const list = out[f.name] || (out[f.name] = []);
        while (!sub.eof()) list.push(this.readScalar(sub, f));
        continue;
      }
      const value = this.readField(r, f, wt);
      if (f.repeated) (out[f.name] || (out[f.name] = [])).push(value);
      else out[f.name] = value;
    }
    this.fillDefaults(fields, out);
    return out;
  }

  readField(r, f, wt) {
    switch (f.kind) {
      case "message": return this.decode(f.type, r.take(Number(r.varint())));
      case "string": return textDecoder.decode(r.take(Number(r.varint())));
      case "bytes": return Uint8Array.from(r.take(Number(r.varint())));
      default:
        if (wt !== wireType(f.kind)) throw new Error(`wire type ${wt} does not match ${f.kind} field ${f.name}`);
        return this.readScalar(r, f);
    }
  }

  readScalar(r, f) {
    const dv = (n) => { const b = r.take(n); return new DataView(b.buffer, b.byteOffset, n); };
    switch (f.kind) {
      case "bool": return r.varint() !== 0n;
      case "enum": case "int32": return Number(BigInt.asIntN(32, r.varint()));
      case "uint32": return Number(BigInt.asUintN(32, r.varint()));
      case "int64": return BigInt.asIntN(64, r.varint()).toString();
      case "uint64": return BigInt.asUintN(64, r.varint()).toString();
      case "sint32": case "sint64": {
        const n = r.varint();
        const v = (n >> 1n) ^ -(n & 1n);
        return f.kind === "sint32" ? Number(v) : v.toString();
      }
      case "float": return Math.round(dv(4).getFloat32(0, true) * 1e4) / 1e4;
      case "fixed32": return dv(4).getUint32(0, true);
      case "sfixed32": return dv(4).getInt32(0, true);
      case "double": return dv(8).getFloat64(0, true);
      case "fixed64": return dv(8).getBigUint64(0, true).toString();
      case "sfixed64": return dv(8).getBigInt64(0, true).toString();
      default: throw new Error("cannot read scalar kind " + f.kind);
    }
  }

  // proto3 omits default values on the wire; fill them so the UI sees every field.
  fillDefaults(fields, out) {
    for (const f of fields) {
      if (out[f.name] !== undefined) continue;
      if (f.repeated) out[f.name] = [];
      else if (f.kind === "message") out[f.name] = null;
      else if (f.kind === "string") out[f.name] = "";
      else if (f.kind === "bool") out[f.name] = false;
      else if (f.kind === "bytes") out[f.name] = new Uint8Array();
      else if (INT64_KINDS.has(f.kind)) out[f.name] = "0";
      else out[f.name] = 0;
    }
  }

  // Returns a copy with enum numbers replaced by "NAME" for display.
  display(typeName, obj) {
    if (!obj) return obj;
    const out = {};
    for (const f of this.fields(typeName)) {
      const v = obj[f.name];
      const one = (x) => f.kind === "enum" ? this.enumName(f.type, x)
        : f.kind === "message" ? this.display(f.type, x) : x;
      out[f.name] = Array.isArray(v) ? v.map(one) : one(v);
    }
    return out;
  }

  // ---------------------------------------------------------------- packets

  // Payload layout: | u32 msg_id | u32 seq | body |, big-endian.
  packet(typeName, seq, obj) {
    const id = this.idByName.get(typeName);
    if (id === undefined) throw new Error("message has no id: " + typeName);
    const body = this.encode(typeName, obj);
    const buf = new Uint8Array(8 + body.length);
    const dv = new DataView(buf.buffer);
    dv.setUint32(0, id, false);
    dv.setUint32(4, seq, false);
    buf.set(body, 8);
    return buf;
  }

  parsePacket(data) {
    const bytes = new Uint8Array(data);
    if (bytes.length < 8) throw new Error("packet shorter than header");
    const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.length);
    const id = dv.getUint32(0, false);
    const seq = dv.getUint32(4, false);
    const typeName = this.nameById.get(id) || null;
    const msg = typeName ? this.decode(typeName, bytes.subarray(8)) : null;
    return { id, seq, typeName, msg };
  }
}

// Short display name: "gs.game.LoginReq" -> "LoginReq".
export const shortName = (full) => (full ? full.slice(full.lastIndexOf(".") + 1) : "?");
