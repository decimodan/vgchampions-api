-- Migration number: 0003 	 2026-05-06T00:00:00.000Z
-- Pokémon catalog (source: docs/pokemon.json — pokemon[].slug/name/types/spriteUrl)

CREATE TABLE IF NOT EXISTS pokemon (
    slug TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    types_json TEXT NOT NULL,
    sprite_url TEXT NOT NULL,
    usage_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_pokemon_name ON pokemon(name);
