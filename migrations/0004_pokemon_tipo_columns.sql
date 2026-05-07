-- Migration number: 0004 	 2026-05-06T00:00:00.000Z
-- Replace types_json with tipo_primario / tipo_secundario (nullable) for filtering and joins

CREATE TABLE pokemon_new (
    slug TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    tipo_primario TEXT NOT NULL,
    tipo_secundario TEXT,
    sprite_url TEXT NOT NULL,
    usage_count INTEGER NOT NULL DEFAULT 0
);

INSERT INTO pokemon_new (slug, name, tipo_primario, tipo_secundario, sprite_url, usage_count)
SELECT
    slug,
    name,
    json_extract(types_json, '$[0].slug'),
    json_extract(types_json, '$[1].slug'),
    sprite_url,
    usage_count
FROM pokemon;

DROP TABLE pokemon;

ALTER TABLE pokemon_new RENAME TO pokemon;

CREATE INDEX IF NOT EXISTS idx_pokemon_name ON pokemon(name);
CREATE INDEX IF NOT EXISTS idx_pokemon_tipo_primario ON pokemon(tipo_primario);
CREATE INDEX IF NOT EXISTS idx_pokemon_tipo_secundario ON pokemon(tipo_secundario);
CREATE INDEX IF NOT EXISTS idx_pokemon_tipos ON pokemon(tipo_primario, tipo_secundario);
