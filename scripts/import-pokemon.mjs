#!/usr/bin/env node
/**
 * One-off import from docs/pokemon.json into table pokemon.
 * Repo root: pnpm import:pokemon | pnpm import:pokemon:remote
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
const ROWS_PER_INSERT = 50;

function sqlLiteral(value) {
	if (value === null || value === undefined) return "NULL";
	return `'${String(value).replace(/'/g, "''")}'`;
}

function readJson(path) {
	return JSON.parse(readFileSync(path, "utf8"));
}

function rowSql(entry) {
	const types = entry.types ?? [];
	const tipo_primario = types[0]?.slug;
	const tipo_secundario = types[1]?.slug ?? null;
	if (tipo_primario == null || tipo_primario === "") {
		throw new Error(`Missing primary type for slug=${entry.slug}`);
	}
	return `(${sqlLiteral(entry.slug)}, ${sqlLiteral(entry.name)}, ${sqlLiteral(tipo_primario)}, ${sqlLiteral(tipo_secundario)}, ${sqlLiteral(entry.spriteUrl)}, ${Number(entry.usageCount ?? 0)})`;
}

function buildSql(list) {
	const lines = ["PRAGMA foreign_keys = ON;", "BEGIN TRANSACTION;"];
	const cols = "slug, name, tipo_primario, tipo_secundario, sprite_url, usage_count";

	for (let i = 0; i < list.length; i += ROWS_PER_INSERT) {
		const chunk = list.slice(i, i + ROWS_PER_INSERT);
		const values = chunk.map(rowSql).join(",\n");
		lines.push(`INSERT OR REPLACE INTO pokemon (${cols}) VALUES\n${values};`);
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
	const path = join(DOCS, "pokemon.json");
	const root = readJson(path);
	const list = root.pokemon;
	if (!Array.isArray(list)) {
		throw new Error(`${path}: expected { pokemon: [...] }`);
	}

	const sql = buildSql(list);
	const tmpFile = join(tmpdir(), `vgchampions-pokemon-${Date.now()}.sql`);
	try {
		writeFileSync(tmpFile, sql, "utf8");
		console.log(`[import-pokemon] ${list.length} rows → executing SQL (${wranglerFlags}) …`);
		runWrangler(tmpFile);
		console.log("[import-pokemon] Done.");
	} finally {
		try {
			unlinkSync(tmpFile);
		} catch {
			// ignore
		}
	}
}

main();
