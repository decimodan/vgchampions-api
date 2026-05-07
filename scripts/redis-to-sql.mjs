#!/usr/bin/env node
/**
 * Lee torneos guardados en Redis (mismas claves que fetch-tournament-data.mjs) y los vuelca a
 * PostgreSQL y/o Cloudflare D1 (SQLite). Idempotente: borra standings/fases por torneo antes de reinsertar.
 *
 * Redis:
 *   {prefix}:tournament:{id}:details   — JSON
 *   {prefix}:tournament:{id}:standings — JSON array
 *
 * Variables de entorno:
 *   REDIS_URL (requerido)
 *   REDIS_KEY_PREFIX (default: vgchampions)
 *   DATABASE_URL (requerido si --target incluye postgres; connection string Postgres)
 *
 * CLI:
 *   node scripts/redis-to-sql.mjs --target postgres|d1|both
 *   --input docs/tournaments.json   (opcional: importa lista base antes de enriquecer con Redis)
 *   --d1-remote                     (wrangler d1 contra remoto en lugar de local)
 *   --dry-run                       (solo estadísticas, no escribe ni ejecuta)
 *   --sql-out-prefix path/prefix    (opcional; escribe prefix.d1.sql y/o prefix.postgres.sql)
 *   --dump-only                     (solo escribe archivos desde --sql-out-prefix; no ejecuta)
 *   --no-progress                   (sin barra de progreso; útil si la salida no es una TTY)
 *
 * Postgres: el esquema debe coincidir con migrations/0002_* (tipos SQLite INTEGER/TEXT están bien en PG).
 */

import { execSync } from "node:child_process";
import { readFileSync, writeFileSync, unlinkSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import pg from "pg";

import { RedisMinimal, redisScanAllKeys } from "./lib/redis-minimal.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = join(__dirname, "..");

const TOURNAMENT_ROWS_PER_INSERT = 40;
const STANDING_ROWS_PER_INSERT = 30;

/** @typedef {'d1' | 'postgres'} SqlDialect */

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

function sqlLiteral(value) {
	if (value === null || value === undefined) return "NULL";
	return `'${String(value).replace(/'/g, "''")}'`;
}

function bool01(value) {
	return value ? 1 : 0;
}

function collectOrganizerIds(tournaments) {
	const ids = new Set();
	for (const t of tournaments) {
		if (t.organizerId != null) ids.add(t.organizerId);
	}
	return [...ids];
}

function tournamentRowValuesSqlite(t) {
	return `(${sqlLiteral(t.id)}, ${sqlLiteral(t.game)}, ${sqlLiteral(t.format)}, ${sqlLiteral(t.name)}, ${sqlLiteral(t.date)}, ${Number(t.players)}, ${Number(t.organizerId)}, NULL, NULL, NULL, NULL)`;
}

function tournamentRowValuesPg(t) {
	return `(${sqlLiteral(t.id)}, ${sqlLiteral(t.game)}, ${sqlLiteral(t.format)}, ${sqlLiteral(t.name)}, ${sqlLiteral(t.date)}, ${Number(t.players)}, ${Number(t.organizerId)}, NULL, NULL, NULL, NULL)`;
}

function bulkInsertOrganizersSqlite(ids) {
	if (ids.length === 0) return "";
	const values = ids.map((id) => `(${Number(id)})`).join(", ");
	return `INSERT OR IGNORE INTO organizers (id) VALUES ${values};`;
}

function bulkInsertOrganizersPg(ids) {
	if (ids.length === 0) return "";
	const values = ids.map((id) => `(${Number(id)})`).join(", ");
	return `INSERT INTO organizers (id) VALUES ${values} ON CONFLICT (id) DO NOTHING;`;
}

function bulkReplaceTournamentsSqlite(rows) {
	if (rows.length === 0) return "";
	const cols =
		"id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online";
	const values = rows.map((t) => tournamentRowValuesSqlite(t)).join(",\n");
	return `INSERT OR REPLACE INTO tournaments (${cols}) VALUES\n${values};`;
}

function bulkUpsertTournamentsPg(rows) {
	if (rows.length === 0) return "";
	const cols =
		"id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online";
	const values = rows.map((t) => tournamentRowValuesPg(t)).join(",\n");
	return `INSERT INTO tournaments (${cols}) VALUES\n${values}
ON CONFLICT (id) DO UPDATE SET
  game = EXCLUDED.game,
  format = EXCLUDED.format,
  name = EXCLUDED.name,
  date = EXCLUDED.date,
  players = EXCLUDED.players,
  organizer_id = EXCLUDED.organizer_id,
  platform = EXCLUDED.platform,
  decklists = EXCLUDED.decklists,
  is_public = EXCLUDED.is_public,
  is_online = EXCLUDED.is_online;`;
}

/**
 * @param {SqlDialect} dialect
 */
function appendTournamentMerge(lines, dialect, details, standings) {
	if (!details || typeof details !== "object" || !details.id) {
		console.warn("[redis-to-sql] detalles sin id; se omite");
		return;
	}
	const oid = details.organizer?.id;
	if (oid == null) {
		throw new Error(`Torneo ${details.id}: organizer.id ausente`);
	}
	const oname = details.organizer?.name ?? null;

	if (dialect === "d1") {
		lines.push(
			`UPDATE organizers SET name = ${sqlLiteral(oname ?? null)} WHERE id = ${Number(oid)};`,
		);
		lines.push(
			`INSERT OR REPLACE INTO tournaments (id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online) VALUES (${sqlLiteral(details.id)}, ${sqlLiteral(details.game)}, ${sqlLiteral(details.format)}, ${sqlLiteral(details.name)}, ${sqlLiteral(details.date)}, ${Number(details.players)}, ${Number(oid)}, ${sqlLiteral(details.platform)}, ${bool01(details.decklists)}, ${bool01(details.isPublic)}, ${bool01(details.isOnline)});`,
		);
	} else {
		lines.push(
			`INSERT INTO organizers (id, name) VALUES (${Number(oid)}, ${sqlLiteral(oname)})
ON CONFLICT (id) DO UPDATE SET name = COALESCE(EXCLUDED.name, organizers.name);`,
		);
		lines.push(
			`INSERT INTO tournaments (id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online)
VALUES (${sqlLiteral(details.id)}, ${sqlLiteral(details.game)}, ${sqlLiteral(details.format)}, ${sqlLiteral(details.name)}, ${sqlLiteral(details.date)}, ${Number(details.players)}, ${Number(oid)}, ${sqlLiteral(details.platform)}, ${bool01(details.decklists)}, ${bool01(details.isPublic)}, ${bool01(details.isOnline)})
ON CONFLICT (id) DO UPDATE SET
  game = EXCLUDED.game,
  format = EXCLUDED.format,
  name = EXCLUDED.name,
  date = EXCLUDED.date,
  players = EXCLUDED.players,
  organizer_id = EXCLUDED.organizer_id,
  platform = EXCLUDED.platform,
  decklists = EXCLUDED.decklists,
  is_public = EXCLUDED.is_public,
  is_online = EXCLUDED.is_online;`,
		);
	}

	lines.push(`DELETE FROM tournament_phases WHERE tournament_id = ${sqlLiteral(details.id)};`);
	for (const p of details.phases ?? []) {
		lines.push(
			`INSERT INTO tournament_phases (tournament_id, phase, type, rounds, mode) VALUES (${sqlLiteral(details.id)}, ${Number(p.phase)}, ${sqlLiteral(p.type)}, ${Number(p.rounds)}, ${sqlLiteral(p.mode)});`,
		);
	}

	const tournamentId = details.id;
	if (!Array.isArray(standings) || standings.length === 0) {
		console.warn(`[redis-to-sql] ${tournamentId}: sin standings en Redis (solo phases/torneo)`);
		return;
	}

	lines.push(`DELETE FROM tournament_standings WHERE tournament_id = ${sqlLiteral(tournamentId)};`);
	const placingCol = dialect === "d1" ? "placing" : '"placing"';
	const standingCols = `tournament_id, ${placingCol}, display_name, player_handle, country, wins, losses, ties, drop_round, deck_json, decklist_json`;
	for (let i = 0; i < standings.length; i += STANDING_ROWS_PER_INSERT) {
		const chunk = standings.slice(i, i + STANDING_ROWS_PER_INSERT);
		const values = chunk
			.map((row) => {
				const deckJson = JSON.stringify(row.deck ?? {});
				const decklistJson = JSON.stringify(row.decklist ?? []);
				const drop = row.drop === null || row.drop === undefined ? "NULL" : Number(row.drop);
				return `(${sqlLiteral(tournamentId)}, ${Number(row.placing)}, ${sqlLiteral(row.name)}, ${sqlLiteral(row.player)}, ${sqlLiteral(row.country ?? null)}, ${Number(row.record?.wins ?? 0)}, ${Number(row.record?.losses ?? 0)}, ${Number(row.record?.ties ?? 0)}, ${drop}, ${sqlLiteral(deckJson)}, ${sqlLiteral(decklistJson)})`;
			})
			.join(",\n");
		lines.push(`INSERT INTO tournament_standings (${standingCols}) VALUES\n${values};`);
	}
}

/**
 * @param {SqlDialect} dialect
 * @param {{ tournamentsList: object[], redisPayloads: { id: string, details: object, standings: unknown }[] }} data
 */
function buildFullSql(dialect, { tournamentsList, redisPayloads }) {
	const lines =
		dialect === "d1"
			? ["PRAGMA foreign_keys = ON;", "BEGIN TRANSACTION;"]
			: ["BEGIN;"];

	const organizerIds = collectOrganizerIds(tournamentsList);
	for (const { details } of redisPayloads) {
		if (details?.organizer?.id != null) organizerIds.push(details.organizer.id);
	}
	const uniqueOrgIds = [...new Set(organizerIds)];

	if (dialect === "d1") {
		const orgSql = bulkInsertOrganizersSqlite(uniqueOrgIds);
		if (orgSql) lines.push(orgSql);
		for (let i = 0; i < tournamentsList.length; i += TOURNAMENT_ROWS_PER_INSERT) {
			const chunk = tournamentsList.slice(i, i + TOURNAMENT_ROWS_PER_INSERT);
			lines.push(bulkReplaceTournamentsSqlite(chunk));
		}
	} else {
		const orgSql = bulkInsertOrganizersPg(uniqueOrgIds);
		if (orgSql) lines.push(orgSql);
		for (let i = 0; i < tournamentsList.length; i += TOURNAMENT_ROWS_PER_INSERT) {
			const chunk = tournamentsList.slice(i, i + TOURNAMENT_ROWS_PER_INSERT);
			lines.push(bulkUpsertTournamentsPg(chunk));
		}
	}

	for (const { details, standings } of redisPayloads) {
		appendTournamentMerge(lines, dialect, details, standings);
	}

	lines.push("COMMIT;");
	return lines.join("\n");
}

function escapeRegex(s) {
	return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function tournamentIdFromDetailsKey(key, prefix) {
	const re = new RegExp(`^${escapeRegex(prefix)}:tournament:([^:]+):details$`);
	const m = String(key).match(re);
	return m?.[1] ?? null;
}

function redisTournamentKeys(prefix, id) {
	return {
		details: `${prefix}:tournament:${id}:details`,
		standings: `${prefix}:tournament:${id}:standings`,
	};
}

function parseArgs(argv) {
	const out = {
		target: "both",
		input: null,
		d1Remote: false,
		dryRun: false,
		sqlOutPrefix: null,
		dumpOnly: false,
		noProgress: false,
	};
	for (let i = 2; i < argv.length; i++) {
		const a = argv[i];
		if (a === "--target" && argv[i + 1]) {
			out.target = argv[++i];
		} else if (a === "--input" && argv[i + 1]) {
			out.input = argv[++i];
		} else if (a === "--d1-remote") {
			out.d1Remote = true;
		} else if (a === "--dry-run") {
			out.dryRun = true;
		} else if (a === "--sql-out-prefix" && argv[i + 1]) {
			out.sqlOutPrefix = argv[++i];
		} else if (a === "--dump-only") {
			out.dumpOnly = true;
		} else if (a === "--no-progress") {
			out.noProgress = true;
		}
	}
	return out;
}

/** Progreso en una línea sobre stdout cuando hay TTY. */
function createProgress(show) {
	const ok = Boolean(show && process.stdout.isTTY);
	let drawing = false;
	return {
		/** @param step 1..total */
		tick(step, total, etiqueta = "Leyendo torneos") {
			if (!ok || total <= 0) return;
			drawing = true;
			const w = 28;
			const done = Math.min(w, Math.round((step / total) * w));
			const pct = step >= total ? 100 : Math.min(99, Math.floor((100 * step) / total));
			const bar = "█".repeat(done) + "░".repeat(w - done);
			process.stdout.write(
				`\x1b[2K\r[redis-to-sql] ${etiqueta}: ${bar} ${pct}% (${step}/${total})`,
			);
		},
		finish(step, total, etiqueta = "Leyendo torneos") {
			this.tick(step, total, etiqueta);
			if (ok && drawing) process.stdout.write("\n");
			drawing = false;
		},
		/** antes de console.warn sobre la misma ventana */
		suspendLine() {
			if (!ok || !drawing) return;
			process.stdout.write("\x1b[2K\r");
			drawing = false;
		},
	};
}

function ensureDirForFile(filePath) {
	const d = dirname(filePath);
	if (d && !existsSync(d)) mkdirSync(d, { recursive: true });
}

function runWrangler(sqlPath, remote) {
	const wranglerFlags = remote ? "--remote" : "--local";
	const localWrangler = join(ROOT, "node_modules", ".bin", "wrangler");
	const wranglerCmd = existsSync(localWrangler) ? JSON.stringify(localWrangler) : "npx wrangler";
	const cmd = `${wranglerCmd} d1 execute DB ${wranglerFlags} --file=${JSON.stringify(sqlPath)}`;
	execSync(cmd, { cwd: ROOT, stdio: "inherit", shell: true });
}

async function runPostgres(sql) {
	const url = process.env.DATABASE_URL?.trim();
	if (!url) {
		throw new Error("DATABASE_URL no definida (requerida para --target postgres o both)");
	}
	const client = new pg.Client({ connectionString: url });
	await client.connect();
	try {
		await client.query(sql);
	} finally {
		await client.end();
	}
}

async function main() {
	loadDotEnv(ROOT);
	const args = parseArgs(process.argv);

	const redisUrl = process.env.REDIS_URL?.trim();
	if (!redisUrl) {
		console.error("Define REDIS_URL");
		process.exit(1);
	}
	const prefix = process.env.REDIS_KEY_PREFIX ?? "vgchampions";
	const pattern = `${prefix}:tournament:*:details`;

	let tournamentsList = [];
	if (args.input) {
		const inputPath = resolve(args.input);
		const raw = readFileSync(inputPath, "utf8");
		const list = JSON.parse(raw);
		if (!Array.isArray(list)) throw new Error(`${inputPath}: se esperaba un array`);
		tournamentsList = list;
	}

	const target = args.target.toLowerCase();
	if (!["postgres", "d1", "both"].includes(target)) {
		console.error("--target debe ser postgres | d1 | both");
		process.exit(1);
	}

	const showProgress = !args.noProgress;
	const client = new RedisMinimal(redisUrl);
	await client.connect();
	let detailKeys = [];
	try {
		detailKeys = await redisScanAllKeys(
			client,
			pattern,
			showProgress
				? (n, ongoing) => {
						const suf = ongoing ? "…" : "";
						process.stdout.write(
							`\x1b[2K\r[redis-to-sql] Escaneando claves Redis${suf} ${n} halladas`,
						);
					}
				: null,
		);
	} finally {
		client.close();
	}
	if (showProgress && process.stdout.isTTY) process.stdout.write("\n");

	const idsFromRedis = [
		...new Set(detailKeys.map((k) => tournamentIdFromDetailsKey(k, prefix)).filter(Boolean)),
	].sort();

	console.log(
		`[redis-to-sql] ${detailKeys.length} claves details, ${idsFromRedis.length} torneos únicos (prefix=${prefix})`,
	);

	const redisPayloads = [];
	const prog = createProgress(showProgress);
	const r2 = new RedisMinimal(redisUrl);
	await r2.connect();
	const totalIds = idsFromRedis.length;
	try {
		let step = 0;
		for (const id of idsFromRedis) {
			step++;
			prog.tick(step, totalIds, "Leyendo torneos");
			const { details: dk, standings: sk } = redisTournamentKeys(prefix, id);
			const dRaw = await r2.get(dk);
			const sRaw = await r2.get(sk);
			if (dRaw == null) continue;
			let details;
			try {
				details = JSON.parse(dRaw);
			} catch {
				prog.suspendLine();
				console.warn(`[redis-to-sql] JSON inválido en details ${id}`);
				continue;
			}
			let standings = null;
			if (sRaw != null) {
				try {
					standings = JSON.parse(sRaw);
				} catch {
					prog.suspendLine();
					console.warn(`[redis-to-sql] JSON inválido en standings ${id}`);
				}
			}
			redisPayloads.push({ id, details, standings });
		}
		if (totalIds > 0) prog.finish(step, totalIds, "Leyendo torneos");
	} finally {
		r2.close();
	}

	if (redisPayloads.length === 0) {
		console.warn("[redis-to-sql] No hay datos válidos en Redis; nada que migrar.");
		process.exit(0);
	}

	if (args.dryRun) {
		console.log(
			`[dry-run] list JSON: ${tournamentsList.length} filas | Redis payloads: ${redisPayloads.length}`,
		);
		return;
	}

	const runD1 = target === "d1" || target === "both";
	const runPg = target === "postgres" || target === "both";

	if (args.dumpOnly && !args.sqlOutPrefix) {
		console.error("--dump-only requiere --sql-out-prefix");
		process.exit(1);
	}

	if (runD1) {
		const sql = buildFullSql("d1", { tournamentsList, redisPayloads });
		const persistentPath =
			args.sqlOutPrefix && (target === "d1" || target === "both")
				? resolve(`${args.sqlOutPrefix}.d1.sql`)
				: null;
		let tmpFile = persistentPath;
		if (!tmpFile) tmpFile = join(tmpdir(), `vgchampions-redis-d1-${Date.now()}.sql`);
		else ensureDirForFile(tmpFile);
		writeFileSync(tmpFile, sql, "utf8");
		if (persistentPath) {
			console.log(`[redis-to-sql] D1 SQL → ${persistentPath}`);
		}
		if (!args.dumpOnly) {
			console.log(`[redis-to-sql] D1 (${args.d1Remote ? "remote" : "local"}) …`);
			runWrangler(tmpFile, args.d1Remote);
		}
		if (!persistentPath) {
			try {
				unlinkSync(tmpFile);
			} catch {
				/* ignore */
			}
		}
	}

	if (runPg) {
		const sql = buildFullSql("postgres", { tournamentsList, redisPayloads });
		const pgPath =
			args.sqlOutPrefix && (target === "postgres" || target === "both")
				? resolve(`${args.sqlOutPrefix}.postgres.sql`)
				: null;
		if (pgPath) {
			ensureDirForFile(pgPath);
			writeFileSync(pgPath, sql, "utf8");
			console.log(`[redis-to-sql] Postgres SQL → ${pgPath}`);
		}
		if (!args.dumpOnly) {
			console.log("[redis-to-sql] PostgreSQL …");
			await runPostgres(sql);
			console.log("[redis-to-sql] PostgreSQL terminado.");
		}
	}
}

main().catch((e) => {
	console.error(e);
	process.exit(1);
});
