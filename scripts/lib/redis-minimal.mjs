/**
 * Cliente Redis RESP2 mínimo (sin dependencias), alineado con fetch-tournament-data.mjs.
 */
import net from "node:net";
import tls from "node:tls";

export function encodeArgv(argv) {
	const parts = [Buffer.from(`*${argv.length}\r\n`)];
	for (const arg of argv) {
		const buf = Buffer.from(String(arg), "utf8");
		parts.push(Buffer.from(`$${buf.length}\r\n`));
		parts.push(buf);
		parts.push(Buffer.from("\r\n"));
	}
	return Buffer.concat(parts);
}

/** @returns {{ value: unknown, next: number } | null} */
export function tryParseValue(buf, off = 0) {
	if (off >= buf.length) return null;
	const type = buf[off];
	if (type === 43) {
		const end = buf.indexOf("\r\n", off);
		if (end === -1) return null;
		return { value: buf.slice(off + 1, end).toString("utf8"), next: end + 2 };
	}
	if (type === 45) {
		const end = buf.indexOf("\r\n", off);
		if (end === -1) return null;
		const msg = buf.slice(off + 1, end).toString("utf8");
		const err = new Error(msg);
		err.redisError = true;
		throw err;
	}
	if (type === 58) {
		const end = buf.indexOf("\r\n", off);
		if (end === -1) return null;
		const n = Number(buf.slice(off + 1, end));
		return { value: n, next: end + 2 };
	}
	if (type === 36) {
		const lineEnd = buf.indexOf("\r\n", off);
		if (lineEnd === -1) return null;
		const len = Number(buf.slice(off + 1, lineEnd));
		if (len === -1) return { value: null, next: lineEnd + 2 };
		const dataStart = lineEnd + 2;
		const dataEnd = dataStart + len;
		if (buf.length < dataEnd + 2) return null;
		return { value: buf.slice(dataStart, dataEnd).toString("utf8"), next: dataEnd + 2 };
	}
	if (type === 42) {
		const lineEnd = buf.indexOf("\r\n", off);
		if (lineEnd === -1) return null;
		const count = Number(buf.slice(off + 1, lineEnd));
		let pos = lineEnd + 2;
		const arr = [];
		for (let i = 0; i < count; i++) {
			const sub = tryParseValue(buf, pos);
			if (!sub) return null;
			arr.push(sub.value);
			pos = sub.next;
		}
		return { value: arr, next: pos };
	}
	throw new Error(`RESP desconocido en offset ${off}`);
}

export function parseRedisUrl(urlStr) {
	const u = new URL(urlStr);
	const useTls = u.protocol === "rediss:";
	const host = u.hostname || "127.0.0.1";
	const port = u.port ? Number(u.port) : 6379;
	const username = u.username ? decodeURIComponent(u.username) : "";
	const password = u.password ? decodeURIComponent(u.password) : "";
	let db = 0;
	if (u.pathname && u.pathname.length > 1) {
		db = Number(u.pathname.slice(1).split("/")[0]) || 0;
	}
	return { useTls, host, port, username, password, db };
}

/** RFC 6066 prohíbe usar una IP como TLS SNI; Node emite DEP0123 si pasas servername con IP. */
function tlsRedisOptions(host, port) {
	const opts = { host, port };
	if (net.isIP(host) === 0) opts.servername = host;
	return opts;
}

export class RedisMinimal {
	constructor(urlString) {
		this.urlString = urlString;
		this.buf = Buffer.alloc(0);
		this.socket = null;
		this.lock = Promise.resolve();
	}

	async connect() {
		const o = parseRedisUrl(this.urlString);
		this.socket = o.useTls
			? tls.connect(tlsRedisOptions(o.host, o.port))
			: net.connect({ host: o.host, port: o.port });
		await new Promise((res, rej) => {
			this.socket.once("connect", res);
			this.socket.once("error", rej);
		});
		if (o.password) {
			if (o.username) await this.command("AUTH", o.username, o.password);
			else await this.command("AUTH", o.password);
		}
		if (o.db > 0) await this.command("SELECT", String(o.db));
	}

	async command(...argv) {
		const prev = this.lock;
		let done;
		this.lock = new Promise((r) => {
			done = r;
		});
		await prev;
		try {
			this.socket.write(encodeArgv(argv));
			while (true) {
				const r = tryParseValue(this.buf, 0);
				if (r) {
					this.buf = this.buf.slice(r.next);
					return r.value;
				}
				const chunk = await new Promise((res, rej) => {
					this.socket.once("data", res);
					this.socket.once("error", rej);
				});
				this.buf = Buffer.concat([this.buf, chunk]);
			}
		} finally {
			done();
		}
	}

	async get(key) {
		return this.command("GET", key);
	}

	close() {
		try {
			this.socket?.destroy();
		} catch {
			// ignore
		}
		this.socket = null;
	}
}

/**
 * @param {RedisMinimal} client
 * @param {string} pattern
 * @param {(keysSeen: number, ongoing: boolean) => void} [onProgress] — llamado tras cada SCAN; ongoing=false cuando terminó la pasada Redis
 * @returns {Promise<string[]>}
 */
export async function redisScanAllKeys(client, pattern, onProgress = null) {
	let cursor = "0";
	const out = [];
	do {
		const parts = await client.command("SCAN", cursor, "MATCH", pattern, "COUNT", "512");
		cursor = String(parts[0]);
		const keys = parts[1];
		if (Array.isArray(keys)) out.push(...keys.map((k) => String(k)));
		if (onProgress) onProgress(out.length, cursor !== "0");
	} while (cursor !== "0");
	return [...new Set(out)].sort();
}
