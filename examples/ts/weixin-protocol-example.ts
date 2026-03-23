import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";

const DEFAULT_BASE_URL = "https://ilinkai.weixin.qq.com";
const DEFAULT_CDN_BASE_URL = "https://novac2c.cdn.weixin.qq.com/c2c";
const CHANNEL_VERSION = "1.0.2";
const LONG_POLL_TIMEOUT_MS = 35_000;
const API_TIMEOUT_MS = 15_000;
const CONFIG_TIMEOUT_MS = 10_000;
const SESSION_EXPIRED_ERRCODE = -14;

const MessageType = { USER: 1, BOT: 2 } as const;
const MessageState = { FINISH: 2 } as const;
const MessageItemType = { TEXT: 1, IMAGE: 2 } as const;
const UploadMediaType = { IMAGE: 1 } as const;
const TypingStatus = { TYPING: 1, CANCEL: 2 } as const;

type BaseInfo = { channel_version?: string };
type QRCodeResponse = { qrcode: string; qrcode_img_content: string };
type QRStatusResponse = {
  status: "wait" | "scaned" | "confirmed" | "expired" | string;
  bot_token?: string;
  ilink_bot_id?: string;
  baseurl?: string;
  ilink_user_id?: string;
};

type TextItem = { text?: string };
type CDNMedia = { encrypt_query_param?: string; aes_key?: string; encrypt_type?: number };
type ImageItem = { media?: CDNMedia; mid_size?: number; aeskey?: string };
type MessageItem = { type?: number; text_item?: TextItem; image_item?: ImageItem };
type WeixinMessage = {
  seq?: number;
  message_id?: number;
  from_user_id?: string;
  to_user_id?: string;
  client_id?: string;
  create_time_ms?: number;
  update_time_ms?: number;
  message_type?: number;
  message_state?: number;
  item_list?: MessageItem[];
  context_token?: string;
};

type GetUpdatesResponse = {
  ret?: number;
  errcode?: number;
  errmsg?: string;
  msgs?: WeixinMessage[];
  get_updates_buf?: string;
  longpolling_timeout_ms?: number;
};

type GetUploadURLResponse = { upload_param?: string; thumb_upload_param?: string };
type GetConfigResponse = { ret?: number; errmsg?: string; typing_ticket?: string };
type State = {
  accountId?: string;
  userId?: string;
  baseUrl?: string;
  token?: string;
  getUpdatesBuf?: string;
  contextTokens?: Record<string, string>;
};

type UploadedMedia = {
  filekey: string;
  aesKey: Buffer;
  plainSize: number;
  cipherSize: number;
  downloadEncryptedQueryParam: string;
};

class WeixinProtocolClient {
  readonly routeTag = process.env.WEIXIN_ROUTE_TAG?.trim();
  readonly stateFile = process.env.WEIXIN_STATE_FILE?.trim() || path.resolve("examples/ts/weixin-state.json");
  state: State = { contextTokens: {} };
  baseUrl = process.env.WEIXIN_BASE_URL?.trim() || DEFAULT_BASE_URL;
  cdnBaseUrl = process.env.WEIXIN_CDN_BASE_URL?.trim() || DEFAULT_CDN_BASE_URL;
  token = process.env.WEIXIN_BOT_TOKEN?.trim() || "";

  async init(): Promise<void> {
    await this.loadState();
    if (this.state.baseUrl) this.baseUrl = this.state.baseUrl;
    if (!this.token && this.state.token) this.token = this.state.token;
    this.state.contextTokens ??= {};
  }

  baseInfo(): BaseInfo {
    return { channel_version: CHANNEL_VERSION };
  }

  async startQRCodeLogin(botType = process.env.WEIXIN_BOT_TYPE?.trim() || "3"): Promise<QRCodeResponse> {
    const url = new URL("/ilink/bot/get_bot_qrcode", ensureSlash(this.baseUrl));
    url.searchParams.set("bot_type", botType);
    return this.fetchJson(url, { method: "GET", headers: this.routeTag ? { SKRouteTag: this.routeTag } : {} });
  }

  async getQRCodeStatus(qrcode: string): Promise<QRStatusResponse> {
    const url = new URL("/ilink/bot/get_qrcode_status", ensureSlash(this.baseUrl));
    url.searchParams.set("qrcode", qrcode);
    const controller = new AbortController();
    const t = setTimeout(() => controller.abort(), LONG_POLL_TIMEOUT_MS);
    try {
      return await this.fetchJson(url, {
        method: "GET",
        headers: { "iLink-App-ClientVersion": "1", ...(this.routeTag ? { SKRouteTag: this.routeTag } : {}) },
        signal: controller.signal,
      });
    } catch (error) {
      if (error instanceof Error && error.name === "AbortError") return { status: "wait" };
      throw error;
    } finally {
      clearTimeout(t);
    }
  }

  async waitQRCodeLogin(qrcode: string, timeoutMs = 8 * 60_000): Promise<QRStatusResponse> {
    const deadline = Date.now() + timeoutMs;
    const botType = process.env.WEIXIN_BOT_TYPE?.trim() || "3";
    while (Date.now() < deadline) {
      const status = await this.getQRCodeStatus(qrcode);
      if (status.status === "wait") {
        await sleep(1000);
        continue;
      }
      if (status.status === "scaned") {
        console.log("二维码已扫码，等待用户确认");
        await sleep(1000);
        continue;
      }
      if (status.status === "expired") {
        console.log("二维码已过期，重新拉取中...");
        const refreshed = await this.startQRCodeLogin(botType);
        console.log(`new_qrcode=${refreshed.qrcode}`);
        console.log(`new_qrcode_url=${refreshed.qrcode_img_content}`);
        qrcode = refreshed.qrcode;
        continue;
      }
      if (status.status === "confirmed") {
        if (!status.bot_token) throw new Error("confirmed but bot_token missing");
        this.token = status.bot_token;
        this.state.token = status.bot_token;
        this.state.accountId = status.ilink_bot_id;
        this.state.userId = status.ilink_user_id;
        if (status.baseurl) {
          this.baseUrl = status.baseurl;
          this.state.baseUrl = status.baseurl;
        }
        await this.saveState();
        return status;
      }
      throw new Error(`unexpected qr status: ${status.status}`);
    }
    throw new Error("wait qrcode login timeout");
  }

  async pollForever(): Promise<void> {
    if (!this.token) throw new Error("WEIXIN_BOT_TOKEN or saved token required");
    let timeoutMs = LONG_POLL_TIMEOUT_MS;
    for (;;) {
      const resp = await this.getUpdates(timeoutMs);
      if (resp.longpolling_timeout_ms && resp.longpolling_timeout_ms > 0) {
        timeoutMs = resp.longpolling_timeout_ms;
      }
      if (resp.errcode === SESSION_EXPIRED_ERRCODE || resp.ret === SESSION_EXPIRED_ERRCODE) {
        throw new Error(`session expired (${SESSION_EXPIRED_ERRCODE})`);
      }
      if ((resp.ret ?? 0) !== 0 || (resp.errcode ?? 0) !== 0) {
        throw new Error(`getupdates failed ret=${resp.ret} errcode=${resp.errcode} errmsg=${resp.errmsg}`);
      }
      if (resp.get_updates_buf && resp.get_updates_buf !== this.state.getUpdatesBuf) {
        this.state.getUpdatesBuf = resp.get_updates_buf;
        await this.saveState();
      }
      for (const msg of resp.msgs ?? []) {
        if (msg.from_user_id && msg.context_token) {
          this.state.contextTokens![msg.from_user_id] = msg.context_token;
          await this.saveState();
        }
        console.log(JSON.stringify({
          from: msg.from_user_id,
          itemTypes: (msg.item_list ?? []).map((item) => item.type),
          text: extractText(msg),
          hasContextToken: Boolean(msg.context_token),
        }, null, 2));
      }
    }
  }

  async getUpdates(timeoutMs = LONG_POLL_TIMEOUT_MS): Promise<GetUpdatesResponse> {
    const controller = new AbortController();
    const t = setTimeout(() => controller.abort(), timeoutMs);
    try {
      return await this.postJson<GetUpdatesResponse>("/ilink/bot/getupdates", {
        get_updates_buf: this.state.getUpdatesBuf || "",
        base_info: this.baseInfo(),
      }, timeoutMs, controller.signal);
    } catch (error) {
      if (error instanceof Error && error.name === "AbortError") {
        return { ret: 0, msgs: [], get_updates_buf: this.state.getUpdatesBuf || "" };
      }
      throw error;
    } finally {
      clearTimeout(t);
    }
  }

  async sendText(toUserId: string, text: string): Promise<void> {
    const contextToken = this.requireContextToken(toUserId);
    await this.postJson("/ilink/bot/sendmessage", {
      msg: {
        from_user_id: "",
        to_user_id: toUserId,
        client_id: generateClientId(),
        message_type: MessageType.BOT,
        message_state: MessageState.FINISH,
        context_token: contextToken,
        item_list: [{ type: MessageItemType.TEXT, text_item: { text } }],
      },
      base_info: this.baseInfo(),
    });
  }

  async sendImage(toUserId: string, filePath: string, caption = ""): Promise<void> {
    const contextToken = this.requireContextToken(toUserId);
    const uploaded = await this.uploadImage(toUserId, filePath);
    if (caption) await this.sendText(toUserId, caption);
    await this.postJson("/ilink/bot/sendmessage", {
      msg: {
        to_user_id: toUserId,
        client_id: generateClientId(),
        message_type: MessageType.BOT,
        message_state: MessageState.FINISH,
        context_token: contextToken,
        item_list: [{
          type: MessageItemType.IMAGE,
          image_item: {
            media: {
              encrypt_query_param: uploaded.downloadEncryptedQueryParam,
              aes_key: uploaded.aesKey.toString("base64"),
              encrypt_type: 1,
            },
            mid_size: uploaded.cipherSize,
          },
        }],
      },
      base_info: this.baseInfo(),
    });
  }

  async uploadImage(toUserId: string, filePath: string): Promise<UploadedMedia> {
    const plaintext = await fs.readFile(filePath);
    const aesKey = crypto.randomBytes(16);
    const filekey = crypto.randomBytes(16).toString("hex");
    const cipherText = encryptAesEcb(plaintext, aesKey);
    const { upload_param } = await this.postJson<GetUploadURLResponse>("/ilink/bot/getuploadurl", {
      filekey,
      media_type: UploadMediaType.IMAGE,
      to_user_id: toUserId,
      rawsize: plaintext.length,
      rawfilemd5: crypto.createHash("md5").update(plaintext).digest("hex"),
      filesize: cipherText.length,
      no_need_thumb: true,
      aeskey: aesKey.toString("hex"),
      base_info: this.baseInfo(),
    });
    if (!upload_param) throw new Error("getuploadurl returned empty upload_param");
    const downloadEncryptedQueryParam = await this.uploadCipherToCDN(upload_param, filekey, cipherText);
    return {
      filekey,
      aesKey,
      plainSize: plaintext.length,
      cipherSize: cipherText.length,
      downloadEncryptedQueryParam,
    };
  }

  async getTypingTicket(toUserId: string): Promise<string> {
    const contextToken = this.requireContextToken(toUserId);
    const resp = await this.postJson<GetConfigResponse>("/ilink/bot/getconfig", {
      ilink_user_id: toUserId,
      context_token: contextToken,
      base_info: this.baseInfo(),
    }, CONFIG_TIMEOUT_MS);
    if ((resp.ret ?? 0) !== 0) throw new Error(`getconfig failed: ${resp.errmsg}`);
    return resp.typing_ticket || "";
  }

  async sendTyping(toUserId: string, status: number): Promise<void> {
    const typingTicket = await this.getTypingTicket(toUserId);
    await this.postJson("/ilink/bot/sendtyping", {
      ilink_user_id: toUserId,
      typing_ticket: typingTicket,
      status,
      base_info: this.baseInfo(),
    }, CONFIG_TIMEOUT_MS);
  }

  async downloadAndDecryptMedia(encryptQueryParam: string, aesKeyBase64OrHex: string): Promise<Buffer> {
    const url = `${this.cdnBaseUrl.replace(/\/$/, "")}/download?encrypted_query_param=${encodeURIComponent(encryptQueryParam)}`;
    const res = await fetch(url);
    if (!res.ok) throw new Error(`cdn download ${res.status}: ${await res.text()}`);
    const ciphertext = Buffer.from(await res.arrayBuffer());
    const key = decodeMediaAESKey(aesKeyBase64OrHex);
    return decryptAesEcb(ciphertext, key);
  }

  async loadState(): Promise<void> {
    try {
      const raw = await fs.readFile(this.stateFile, "utf8");
      this.state = JSON.parse(raw) as State;
      this.state.contextTokens ??= {};
    } catch {
      this.state = { contextTokens: {} };
    }
  }

  async saveState(): Promise<void> {
    this.state.baseUrl ||= this.baseUrl;
    this.state.token ||= this.token;
    this.state.contextTokens ??= {};
    await fs.mkdir(path.dirname(this.stateFile), { recursive: true });
    await fs.writeFile(this.stateFile, `${JSON.stringify(this.state, null, 2)}\n`, "utf8");
  }

  requireContextToken(toUserId: string): string {
    const token = this.state.contextTokens?.[toUserId];
    if (!token) throw new Error(`missing context_token for ${toUserId}; poll first or edit state file`);
    return token;
  }

  async uploadCipherToCDN(uploadParam: string, filekey: string, cipherText: Buffer): Promise<string> {
    const url = `${this.cdnBaseUrl.replace(/\/$/, "")}/upload?encrypted_query_param=${encodeURIComponent(uploadParam)}&filekey=${encodeURIComponent(filekey)}`;
    let lastError: unknown;
    for (let i = 0; i < 3; i += 1) {
      const res = await fetch(url, { method: "POST", headers: { "Content-Type": "application/octet-stream" }, body: new Uint8Array(cipherText) });
      if (res.status >= 400 && res.status < 500) {
        throw new Error(`cdn upload client error ${res.status}: ${await res.text()}`);
      }
      if (res.status !== 200) {
        lastError = new Error(`cdn upload server error ${res.status}: ${await res.text()}`);
        continue;
      }
      const downloadParam = res.headers.get("x-encrypted-param");
      if (!downloadParam) throw new Error("cdn upload missing x-encrypted-param");
      return downloadParam;
    }
    throw lastError instanceof Error ? lastError : new Error("cdn upload failed after 3 attempts");
  }

  async postJson<T = unknown>(endpoint: string, payload: unknown, timeoutMs = API_TIMEOUT_MS, signal?: AbortSignal): Promise<T> {
    const body = JSON.stringify(payload);
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      AuthorizationType: "ilink_bot_token",
      "Content-Length": String(Buffer.byteLength(body, "utf8")),
      "X-WECHAT-UIN": buildRandomWechatUIN(),
    };
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    if (this.routeTag) headers.SKRouteTag = this.routeTag;

    const controller = signal ? undefined : new AbortController();
    const actualSignal = signal ?? controller!.signal;
    const t = controller ? setTimeout(() => controller.abort(), timeoutMs) : undefined;
    try {
      const res = await fetch(`${this.baseUrl.replace(/\/$/, "")}${endpoint}`, {
        method: "POST",
        headers,
        body,
        signal: actualSignal,
      });
      const raw = await res.text();
      if (!res.ok) throw new Error(`${endpoint} ${res.status}: ${raw}`);
      return (raw ? JSON.parse(raw) : {}) as T;
    } finally {
      if (t) clearTimeout(t);
    }
  }

  async fetchJson<T>(url: URL, init: RequestInit): Promise<T> {
    const res = await fetch(url, init);
    const raw = await res.text();
    if (!res.ok) throw new Error(`${url.pathname} ${res.status}: ${raw}`);
    return JSON.parse(raw) as T;
  }
}

function ensureSlash(v: string): string {
  return v.endsWith("/") ? v : `${v}/`;
}

function buildRandomWechatUIN(): string {
  const n = crypto.randomBytes(4).readUInt32BE(0);
  return Buffer.from(String(n), "utf8").toString("base64");
}

function generateClientId(): string {
  return `openclaw-weixin-${Date.now()}-${crypto.randomUUID()}`;
}

function extractText(msg: WeixinMessage): string {
  for (const item of msg.item_list ?? []) {
    if (item.type === MessageItemType.TEXT) return item.text_item?.text || "";
  }
  return "";
}

function encryptAesEcb(plaintext: Buffer, key: Buffer): Buffer {
  const cipher = crypto.createCipheriv("aes-128-ecb", key, null);
  return Buffer.concat([cipher.update(plaintext), cipher.final()]);
}

function decryptAesEcb(ciphertext: Buffer, key: Buffer): Buffer {
  const decipher = crypto.createDecipheriv("aes-128-ecb", key, null);
  return Buffer.concat([decipher.update(ciphertext), decipher.final()]);
}

function decodeMediaAESKey(value: string): Buffer {
  if (/^[0-9a-fA-F]{32}$/.test(value)) return Buffer.from(value, "hex");
  const decoded = Buffer.from(value, "base64");
  if (decoded.length === 16) return decoded;
  if (decoded.length === 32 && /^[0-9a-fA-F]{32}$/.test(decoded.toString("ascii"))) {
    return Buffer.from(decoded.toString("ascii"), "hex");
  }
  throw new Error(`unsupported aes_key encoding; decoded length=${decoded.length}`);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function main(): Promise<void> {
  const client = new WeixinProtocolClient();
  await client.init();
  const [command, ...rest] = process.argv.slice(2);

  switch (command) {
    case "start-qr": {
      const qr = await client.startQRCodeLogin();
      console.log(`qrcode=${qr.qrcode}`);
      console.log(`qrcode_url=${qr.qrcode_img_content}`);
      break;
    }
    case "wait-qr": {
      const [qrcode] = rest;
      if (!qrcode) throw new Error("usage: node weixin-protocol-example.ts wait-qr <qrcode>");
      const status = await client.waitQRCodeLogin(qrcode);
      console.log(JSON.stringify(status, null, 2));
      break;
    }
    case "poll": {
      await client.pollForever();
      break;
    }
    case "send-text": {
      const [toUserId, ...text] = rest;
      if (!toUserId || text.length === 0) throw new Error("usage: send-text <to_user_id> <text>");
      await client.sendText(toUserId, text.join(" "));
      break;
    }
    case "send-image": {
      const [toUserId, filePath, ...caption] = rest;
      if (!toUserId || !filePath) throw new Error("usage: send-image <to_user_id> <file_path> [caption]");
      await client.sendImage(toUserId, filePath, caption.join(" "));
      break;
    }
    case "typing-on": {
      const [toUserId] = rest;
      if (!toUserId) throw new Error("usage: typing-on <to_user_id>");
      await client.sendTyping(toUserId, TypingStatus.TYPING);
      break;
    }
    case "typing-off": {
      const [toUserId] = rest;
      if (!toUserId) throw new Error("usage: typing-off <to_user_id>");
      await client.sendTyping(toUserId, TypingStatus.CANCEL);
      break;
    }
    case "download-image": {
      const [encryptQueryParam, aesKey] = rest;
      if (!encryptQueryParam || !aesKey) throw new Error("usage: download-image <encrypt_query_param> <aes_key>");
      const buf = await client.downloadAndDecryptMedia(encryptQueryParam, aesKey);
      process.stdout.write(buf);
      break;
    }
    default:
      console.error(`Usage:
  node weixin-protocol-example.ts start-qr
  node weixin-protocol-example.ts wait-qr <qrcode>
  node weixin-protocol-example.ts poll
  node weixin-protocol-example.ts send-text <to_user_id> <text>
  node weixin-protocol-example.ts send-image <to_user_id> <file_path> [caption]
  node weixin-protocol-example.ts typing-on <to_user_id>
  node weixin-protocol-example.ts typing-off <to_user_id>
  node weixin-protocol-example.ts download-image <encrypt_query_param> <aes_key>`);
      process.exitCode = 1;
  }
}

await main();
