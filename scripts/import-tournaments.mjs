#!/usr/bin/env node
/**
 * One-off import into D1 from docs/*.json (tournaments list + optional details + standings).
 * Repo root: pnpm import:tournaments | pnpm import:tournaments:remote
 */

import { execSync } from "node:child_process";
import { readFileSync, writeFileSync, unlinkSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = join(__dirname, "..");
const DOCS = join(ROOT, "docs");

const remote = process.argv.includes("--remote");
const wranglerFlags = remote ? "--remote" : "--local";

/** Multi-row INSERT keeps statement count low (Wrangler/D1 can choke on thousands of single-row INSERTs). */
const TOURNAMENT_ROWS_PER_INSERT = 40;
const STANDING_ROWS_PER_INSERT = 30;

function sqlLiteral(value) {
	if (value === null || value === undefined) return "NULL";
	return `'${String(value).replace(/'/g, "''")}'`;
}

function bool01(value) {
	return value ? 1 : 0;
}

function readJson(path) {
	return JSON.parse(readFileSync(path, "utf8"));
}

function collectOrganizerIds(tournaments) {
	const ids = new Set();
	for (const t of tournaments) {
		if (t.organizerId != null) ids.add(t.organizerId);
	}
	return [...ids];
}

function tournamentRowValues(t) {
	return `(${sqlLiteral(t.id)}, ${sqlLiteral(t.game)}, ${sqlLiteral(t.format)}, ${sqlLiteral(t.name)}, ${sqlLiteral(t.date)}, ${Number(t.players)}, ${Number(t.organizerId)}, NULL, NULL, NULL, NULL)`;
}

function bulkInsertOrganizers(ids) {
	if (ids.length === 0) return "";
	const values = ids.map((id) => `(${Number(id)})`).join(", ");
	return `INSERT OR IGNORE INTO organizers (id) VALUES ${values};`;
}

function bulkReplaceTournaments(rows) {
	if (rows.length === 0) return "";
	const cols =
		"id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online";
	const values = rows.map((t) => tournamentRowValues(t)).join(",\n");
	return `INSERT OR REPLACE INTO tournaments (${cols}) VALUES\n${values};`;
}

function buildSql({ tournaments, details, standings }) {
	const lines = ["PRAGMA foreign_keys = ON;", "BEGIN TRANSACTION;"];

	const organizerIds = collectOrganizerIds(tournaments);
	if (details?.organizer?.id != null) organizerIds.push(details.organizer.id);
	const uniqueOrgIds = [...new Set(organizerIds)];

	const orgSql = bulkInsertOrganizers(uniqueOrgIds);
	if (orgSql) lines.push(orgSql);

	for (let i = 0; i < tournaments.length; i += TOURNAMENT_ROWS_PER_INSERT) {
		const chunk = tournaments.slice(i, i + TOURNAMENT_ROWS_PER_INSERT);
		lines.push(bulkReplaceTournaments(chunk));
	}

	if (details && typeof details === "object" && details.id) {
		const oid = details.organizer?.id;
		if (oid == null) {
			throw new Error("details.json must include organizer.id");
		}
		const oname = details.organizer?.name;
		lines.push(
			`UPDATE organizers SET name = ${sqlLiteral(oname ?? null)} WHERE id = ${Number(oid)};`,
		);

		lines.push(
			`INSERT OR REPLACE INTO tournaments (id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online) VALUES (${sqlLiteral(details.id)}, ${sqlLiteral(details.game)}, ${sqlLiteral(details.format)}, ${sqlLiteral(details.name)}, ${sqlLiteral(details.date)}, ${Number(details.players)}, ${Number(oid)}, ${sqlLiteral(details.platform)}, ${bool01(details.decklists)}, ${bool01(details.isPublic)}, ${bool01(details.isOnline)});`,
		);

		lines.push(`DELETE FROM tournament_phases WHERE tournament_id = ${sqlLiteral(details.id)};`);
		for (const p of details.phases ?? []) {
			lines.push(
				`INSERT INTO tournament_phases (tournament_id, phase, type, rounds, mode) VALUES (${sqlLiteral(details.id)}, ${Number(p.phase)}, ${sqlLiteral(p.type)}, ${Number(p.rounds)}, ${sqlLiteral(p.mode)});`,
			);
		}
	}

	if (Array.isArray(standings) && standings.length > 0) {
		const tournamentId = details?.id;
		if (!tournamentId) {
			console.warn(
				"[import-tournaments] standings.json present but no details.json with id; skipping standings.",
			);
		} else {
			lines.push(`DELETE FROM tournament_standings WHERE tournament_id = ${sqlLiteral(tournamentId)};`);
			const standingCols =
				"tournament_id, placing, display_name, player_handle, country, wins, losses, ties, drop_round, deck_json, decklist_json";
			for (let i = 0; i < standings.length; i += STANDING_ROWS_PER_INSERT) {
				const chunk = standings.slice(i, i + STANDING_ROWS_PER_INSERT);
				const values = chunk
					.map((row) => {
						const deckJson = JSON.stringify(row.deck ?? {});
						const decklistJson = JSON.stringify(row.decklist ?? []);
						const drop =
							row.drop === null || row.drop === undefined ? "NULL" : Number(row.drop);
						return `(${sqlLiteral(tournamentId)}, ${Number(row.placing)}, ${sqlLiteral(row.name)}, ${sqlLiteral(row.player)}, ${sqlLiteral(row.country ?? null)}, ${Number(row.record?.wins ?? 0)}, ${Number(row.record?.losses ?? 0)}, ${Number(row.record?.ties ?? 0)}, ${drop}, ${sqlLiteral(deckJson)}, ${sqlLiteral(decklistJson)})`;
					})
					.join(",\n");
				lines.push(`INSERT INTO tournament_standings (${standingCols}) VALUES\n${values};`);
			}
		}
	}

	lines.push("COMMIT;");
	return lines.join("\n");
}

function runWrangler(sqlPath) {
	const localWrangler = join(ROOT, "node_modules", ".bin", "wrangler");
	const wranglerCmd = existsSync(localWrangler) ? JSON.stringify(localWrangler) : "npx wrangler";
	const cmd = `${wranglerCmd} d1 execute DB ${wranglerFlags} --file=${JSON.stringify(sqlPath)}`;
	execSync(cmd, { cwd: ROOT, stdio: "inherit", shell: true });
}

function main() {
	const tournamentsPath = join(DOCS, "tournaments.json");
	const tournaments = readJson(tournamentsPath);
	if (!Array.isArray(tournaments)) {
		throw new Error(`${tournamentsPath} must be a JSON array`);
	}

	let details;
	const detailsPath = join(DOCS, "details.json");
	try {
		details = readJson(detailsPath);
	} catch {
		console.warn("[import-tournaments] No details.json; phases/extra columns unchanged except list import.");
	}

	let standings;
	const standingsPath = join(DOCS, "standings.json");
	try {
		standings = readJson(standingsPath);
		if (!Array.isArray(standings)) standings = undefined;
	} catch {
		standings = undefined;
	}

	const sql = buildSql({ tournaments, details, standings });
	const tmpFile = join(tmpdir(), `vgchampions-import-${Date.now()}.sql`);
	try {
		writeFileSync(tmpFile, sql, "utf8");
		console.log(
			`[import-tournaments] ${tournaments.length} tournaments → executing SQL (${wranglerFlags}) …`,
		);
		runWrangler(tmpFile);
		console.log("[import-tournaments] Done.");
	} finally {
		try {
			unlinkSync(tmpFile);
		} catch {
			// ignore
		}
	}
}

main();
