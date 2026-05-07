#!/usr/bin/env node
/**
 * Archivo único: copia este .mjs donde quieras (NAS, VPS, etc.) y ejecuta con Node 18+.
 * Sin package.json ni npm install — solo usa módulos incorporados (net, tls, fs).
 *
 * Variables de entorno (fetch HTTP):
 *   CHAMPIONS_DETAILS_URL_TEMPLATE   URL con {id}
 *   CHAMPIONS_STANDINGS_URL_TEMPLATE URL con {id}
 *
 * Límite de velocidad (429): más reintentos y backoff largo; se usa Retry-After si viene en la respuesta.
 *   CHAMPIONS_DELAY_MS, CHAMPIONS_GAP_MS_BETWEEN_REQUESTS (entre details y standings),
 *   CHAMPIONS_MAX_RETRIES (default 12), CHAMPIONS_429_MIN_WAIT_MS (default 10000),
 *   CHAMPIONS_429_MAX_WAIT_MS (default 300000), CHAMPIONS_TIMEOUT_MS
 *
 * Archivo `.env` en el cwd (opcional): mismas variables, una por línea `CLAVE=valor`,
 * sin `export`. Comentarios con `#`. No sobrescribe variables ya definidas en el shell.
 *
 * Redis (opcional): REDIS_URL definido → guarda strings JSON en Redis (por defecto sin disco).
 *   REDIS_URL, REDIS_KEY_PREFIX (default vgchampions), REDIS_TTL_SECONDS opcional
 *
 * Archivos si no hay REDIS_URL, o además con CHAMPIONS_SAVE_FILES=1 / --also-files:
 *   Lista de torneos: ./tournaments.json o ./docs/tournaments.json (primero que exista),
 *   o --input / CHAMPIONS_INPUT_JSON con ruta absoluta.
 *   Salida por defecto: ./data/scrape (relativo al cwd)
 *
 * CLI: [--input path] [--out dir] [--concurrency n] [--delay-ms n]
 *      [--skip-existing] [--force] [--dry-run] [--also-files]
 */

import net from "node:net";
import tls from "node:tls";
import { mkdirSync, writeFileSync, readFileSync, existsSync, appendFileSync } from "node:fs";
import { join, resolve } from "node:path";

// ─── Redis mínimo (RESP2) ─────────────────────────────────────────────────────

function encodeArgv(argv) {
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
function tryParseValue(buf, off = 0) {
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

function parseRedisUrl(urlStr) {
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

class RedisMinimal {
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
				try {
					const r = tryParseValue(this.buf, 0);
					if (r) {
						this.buf = this.buf.slice(r.next);
						return r.value;
					}
				} catch (e) {
					throw e;
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

	async exists(...keys) {
		if (keys.length === 0) return 0;
		const v = await this.command("EXISTS", ...keys);
		return typeof v === "number" ? v : 0;
	}

	async set(key, value, ttlSeconds) {
		if (ttlSeconds != null && ttlSeconds > 0) {
			return this.command("SET", key, value, "EX", String(ttlSeconds));
		}
		return this.command("SET", key, value);
	}

	async rPush(key, value) {
		return this.command("RPUSH", key, value);
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

// ─── Fetch torneos ───────────────────────────────────────────────────────────

function parseArgs(argv) {
	const out = { _: [] };
	for (let i = 2; i < argv.length; i++) {
		const a = argv[i];
		if (a === "--input") out.input = argv[++i];
		else if (a === "--out") out.out = argv[++i];
		else if (a === "--concurrency") out.concurrency = Number(argv[++i]);
		else if (a === "--delay-ms") out.delayMs = Number(argv[++i]);
		else if (a === "--skip-existing") out.skipExisting = true;
		else if (a === "--force") out.force = true;
		else if (a === "--dry-run") out.dryRun = true;
		else if (a === "--also-files") out.alsoFiles = true;
		else if (a.startsWith("--")) throw new Error(`Flag desconocida: ${a}`);
		else out._.push(a);
	}
	return out;
}

function sleep(ms) {
	return new Promise((r) => setTimeout(r, ms));
}

function expandTemplate(template, id) {
	if (!template.includes("{id}")) throw new Error('La URL debe contener el texto literal "{id}"');
	return template.split("{id}").join(encodeURIComponent(id));
}

function parseHeadersJson() {
	const raw = process.env.CHAMPIONS_FETCH_HEADERS_JSON;
	if (!raw) return {};
	try {
		const o = JSON.parse(raw);
		if (o === null || typeof o !== "object" || Array.isArray(o)) return {};
		return o;
	} catch (e) {
		throw new Error(`CHAMPIONS_FETCH_HEADERS_JSON inválido: ${e.message}`);
	}
}

function parseRetryAfterMs(headers) {
	const raw = headers.get("retry-after");
	if (!raw) return null;
	const sec = Number.parseInt(raw, 10);
	if (!Number.isFinite(sec) || sec < 0) return null;
	return sec * 1000;
}

async function fetchWithRetry(url, init, opts) {
	const {
		timeoutMs,
		maxRetries,
		min429WaitMs,
		max429WaitMs,
	} = opts;
	let lastErr;
	for (let attempt = 0; attempt < maxRetries; attempt++) {
		const controller = new AbortController();
		const t = setTimeout(() => controller.abort(), timeoutMs);
		try {
			const res = await fetch(url, { ...init, signal: controller.signal });
			clearTimeout(t);
			if (res.ok) return res;
			const retryAfterMs = parseRetryAfterMs(res.headers);
			const bodyPreview = (await res.text()).slice(0, 500);
			const err = new Error(`HTTP ${res.status} ${res.statusText}: ${bodyPreview}`);
			err.status = res.status;
			if (res.status === 429 || res.status >= 500) {
				lastErr = err;
				let waitMs;
				if (res.status === 429) {
					const expo = min429WaitMs * 2 ** attempt;
					waitMs = Math.max(retryAfterMs ?? 0, expo);
					waitMs = Math.min(max429WaitMs, waitMs);
				} else {
					waitMs = Math.min(30_000, 1000 * 2 ** attempt);
				}
				console.warn(
					`[HTTP ${res.status}] esperando ${Math.round(waitMs / 1000)}s (intento ${attempt + 1}/${maxRetries})…`,
				);
				await sleep(waitMs);
				continue;
			}
			throw err;
		} catch (e) {
			clearTimeout(t);
			lastErr = e;
			if (e.name === "AbortError") lastErr = new Error(`Timeout después de ${timeoutMs}ms`);
			await sleep(Math.min(30_000, 1000 * 2 ** attempt));
		}
	}
	throw lastErr;
}

async function fetchJson(url, init, opts) {
	const res = await fetchWithRetry(url, init, opts);
	const text = await res.text();
	try {
		return JSON.parse(text);
	} catch {
		throw new Error(`La respuesta no es JSON (${url.slice(0, 80)}…): ${text.slice(0, 200)}`);
	}
}

function redisTournamentKeys(prefix, id) {
	return {
		details: `${prefix}:tournament:${id}:details`,
		standings: `${prefix}:tournament:${id}:standings`,
	};
}

function redisErrorsKey(prefix) {
	return `${prefix}:fetch-errors`;
}

function ensureDirs(dir) {
	mkdirSync(join(dir, "details"), { recursive: true });
	mkdirSync(join(dir, "standings"), { recursive: true });
}

function logErrorFile(outDir, payload) {
	appendFileSync(join(outDir, "fetch-errors.jsonl"), `${JSON.stringify(payload)}\n`);
}

async function processOneTournament(id, ctx) {
	const paths = {
		details: join(ctx.outDir, "details", `${id}.json`),
		standings: join(ctx.outDir, "standings", `${id}.json`),
	};
	const rkeys = ctx.redis ? redisTournamentKeys(ctx.redis.prefix, id) : null;

	if (ctx.skipExisting && !ctx.force) {
		if (ctx.redis) {
			const n = await ctx.redis.client.exists(rkeys.details, rkeys.standings);
			if (n === 2) {
				console.log(`[skip] ${id} (Redis: ya existen details y standings)`);
				return { id, skipped: true };
			}
		} else if (existsSync(paths.details) && existsSync(paths.standings)) {
			console.log(`[skip] ${id} (archivos ya existentes)`);
			return { id, skipped: true };
		}
	}

	const detailsUrl = expandTemplate(ctx.detailsTemplate, id);
	const standingsUrl = expandTemplate(ctx.standingsTemplate, id);

	if (ctx.dryRun) {
		console.log(`[dry-run] ${id}\n  ${detailsUrl}\n  ${standingsUrl}`);
		return { id, dryRun: true };
	}

	const init = {
		method: "GET",
		headers: {
			accept: "application/json",
			...ctx.headers,
		},
	};

	const needDetails =
		ctx.force ||
		(ctx.redis ? (await ctx.redis.client.exists(rkeys.details)) === 0 : !existsSync(paths.details));
	const needStandings =
		ctx.force ||
		(ctx.redis ? (await ctx.redis.client.exists(rkeys.standings)) === 0 : !existsSync(paths.standings));

	try {
		const ttl = ctx.redis?.ttlSeconds;

		if (needDetails) {
			const details = await fetchJson(detailsUrl, init, ctx.fetchOpts);
			const raw = `${JSON.stringify(details, null, 2)}\n`;
			if (ctx.redis) await ctx.redis.client.set(rkeys.details, raw.trimEnd(), ttl);
			if (ctx.saveFiles) writeFileSync(paths.details, raw, "utf8");
		}
		if (ctx.gapBetweenRequestsMs > 0 && needDetails && needStandings) {
			await sleep(ctx.gapBetweenRequestsMs);
		}
		if (needStandings) {
			const standings = await fetchJson(standingsUrl, init, ctx.fetchOpts);
			const raw = `${JSON.stringify(standings, null, 2)}\n`;
			if (ctx.redis) await ctx.redis.client.set(rkeys.standings, raw.trimEnd(), ttl);
			if (ctx.saveFiles) writeFileSync(paths.standings, raw, "utf8");
		}
		console.log(`[ok] ${id}`);
		return { id, ok: true };
	} catch (e) {
		const payload = {
			ts: new Date().toISOString(),
			id,
			message: e.message,
			status: e.status,
		};
		if (ctx.redis) {
			try {
				await ctx.redis.client.rPush(ctx.redis.errorsKey, JSON.stringify(payload));
			} catch {
				/* ignore */
			}
		}
		if (ctx.saveFiles) logErrorFile(ctx.outDir, payload);
		console.error(`[fail] ${id}: ${e.message}`);
		return { id, ok: false, error: e.message };
	}
}

async function runPool(ids, concurrency, delayMs, worker) {
	let ix = 0;
	const n = ids.length;
	const workers = Math.min(concurrency, Math.max(1, n));
	await Promise.all(
		Array.from({ length: workers }, async () => {
			while (true) {
				const idx = ix++;
				if (idx >= n) break;
				if (delayMs > 0 && idx > 0) await sleep(delayMs);
				await worker(ids[idx]);
			}
		}),
	);
}

/** Carga `./.env` si existe (no pisa variables ya definidas en el proceso). */
function loadDotEnv(cwd) {
	const path = join(cwd, ".env");
	if (!existsSync(path)) return;
	const text = readFileSync(path, "utf8");
	for (const line of text.split("\n")) {
		const t = line.trim();
		if (!t || t.startsWith("#")) continue;
		const eq = t.indexOf("=");
		if (eq <= 0) continue;
		const key = t.slice(0, eq).trim();
		if (!key) continue;
		let val = t.slice(eq + 1).trim();
		if (
			(val.startsWith('"') && val.endsWith('"')) ||
			(val.startsWith("'") && val.endsWith("'"))
		) {
			val = val.slice(1, -1);
		}
		if (process.env[key] === undefined) process.env[key] = val;
	}
}

function resolveInputPath(cwd, args) {
	const explicit = args.input ?? process.env.CHAMPIONS_INPUT_JSON;
	if (explicit) {
		const p = resolve(explicit);
		if (!existsSync(p)) {
			console.error(`No existe el archivo de entrada:\n  ${p}\nComprueba la ruta o --input.`);
			process.exit(1);
		}
		return p;
	}
	const candidates = [join(cwd, "tournaments.json"), join(cwd, "docs", "tournaments.json")];
	for (const p of candidates) {
		const abs = resolve(p);
		if (existsSync(abs)) return abs;
	}
	console.error(
		"No se encontró lista de torneos. Coloca tournaments.json en esta carpeta, o:\n" +
			`  ${resolve(join(cwd, "tournaments.json"))}\n` +
			`  ${resolve(join(cwd, "docs", "tournaments.json"))}\n` +
			"O indica la ruta explícita:\n" +
			"  node vgchampions.mjs --input /ruta/a/tournaments.json\n" +
			"  export CHAMPIONS_INPUT_JSON=/ruta/a/tournaments.json",
	);
	process.exit(1);
}

async function main() {
	const args = parseArgs(process.argv);
	const cwd = process.cwd();
	loadDotEnv(cwd);

	const detailsTemplate = process.env.CHAMPIONS_DETAILS_URL_TEMPLATE;
	const standingsTemplate = process.env.CHAMPIONS_STANDINGS_URL_TEMPLATE;
	if (!args.dryRun) {
		if (!detailsTemplate || !standingsTemplate) {
			console.error(
				"Define CHAMPIONS_DETAILS_URL_TEMPLATE y CHAMPIONS_STANDINGS_URL_TEMPLATE (con {id}).",
			);
			process.exit(1);
		}
	}

	const redisUrl = process.env.REDIS_URL?.trim();
	const useRedis = Boolean(redisUrl);
	const saveFiles =
		!useRedis ||
		args.alsoFiles ||
		process.env.CHAMPIONS_SAVE_FILES === "1" ||
		process.env.CHAMPIONS_SAVE_FILES === "true";

	const inputPath = resolveInputPath(cwd, args);
	const raw = readFileSync(inputPath, "utf8");
	const list = JSON.parse(raw);
	if (!Array.isArray(list)) throw new Error(`${inputPath}: se esperaba un array JSON`);

	const ids = [...new Set(list.map((t) => t.id).filter(Boolean))];
	const outDir = resolve(args.out ?? process.env.CHAMPIONS_OUTPUT_DIR ?? join(cwd, "data", "scrape"));

	const concurrency = Math.max(
		1,
		args.concurrency ?? Number(process.env.CHAMPIONS_CONCURRENCY ?? 1),
	);
	const delayMs = args.delayMs ?? Number(process.env.CHAMPIONS_DELAY_MS ?? 0);
	const timeoutMs = Number(process.env.CHAMPIONS_TIMEOUT_MS ?? 60_000);
	const maxRetries = Number(process.env.CHAMPIONS_MAX_RETRIES ?? 12);
	const min429WaitMs = Number(process.env.CHAMPIONS_429_MIN_WAIT_MS ?? 10_000);
	const max429WaitMs = Number(process.env.CHAMPIONS_429_MAX_WAIT_MS ?? 300_000);
	const gapBetweenRequestsMs = Number(process.env.CHAMPIONS_GAP_MS_BETWEEN_REQUESTS ?? 0);

	const redisPrefix = process.env.REDIS_KEY_PREFIX ?? "vgchampions";
	const ttlRaw = process.env.REDIS_TTL_SECONDS;
	const ttlSeconds = ttlRaw != null && ttlRaw !== "" ? Number(ttlRaw) : undefined;

	if (!args.dryRun && saveFiles) ensureDirs(outDir);

	let redisConn;
	let redisCtx;
	if (!args.dryRun && useRedis) {
		redisConn = new RedisMinimal(redisUrl);
		await redisConn.connect();
		redisCtx = {
			client: redisConn,
			prefix: redisPrefix,
			errorsKey: redisErrorsKey(redisPrefix),
			ttlSeconds,
		};
		console.log(
			`[fetch-tournament-data] Redis (${redisPrefix}${ttlSeconds ? `, TTL ${ttlSeconds}s` : ""})${saveFiles ? " + archivos" : ""}`,
		);
	}

	const ctx = {
		outDir,
		detailsTemplate: detailsTemplate ?? "",
		standingsTemplate: standingsTemplate ?? "",
		headers: parseHeadersJson(),
		skipExisting: args.skipExisting ?? false,
		force: args.force ?? false,
		dryRun: args.dryRun ?? false,
		fetchOpts: { timeoutMs, maxRetries, min429WaitMs, max429WaitMs },
		gapBetweenRequestsMs,
		redis: redisCtx,
		saveFiles,
	};

	const dest = useRedis ? `redis:${redisPrefix}` : outDir;
	console.log(
		`[fetch-tournament-data] ${ids.length} torneos → ${dest} (concurrencia=${concurrency}, delayMs=${delayMs})`,
	);

	try {
		await runPool(ids, concurrency, delayMs, (id) => processOneTournament(id, ctx));
	} finally {
		redisConn?.close();
	}

	console.log("[fetch-tournament-data] terminado.");
}

main().catch((e) => {
	console.error(e);
	process.exit(1);
});
