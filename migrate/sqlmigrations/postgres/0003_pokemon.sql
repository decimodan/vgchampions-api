-- Catálogo Pokémon (equiv. D1 después de migrations/0004)

CREATE TABLE IF NOT EXISTS pokemon (
    slug TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    tipo_primario TEXT NOT NULL,
    tipo_secundario TEXT,
    sprite_url TEXT NOT NULL,
    usage_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_pokemon_name ON pokemon (name);

CREATE INDEX IF NOT EXISTS idx_pokemon_tipo_primario ON pokemon (tipo_primario);

CREATE INDEX IF NOT EXISTS idx_pokemon_tipo_secundario ON pokemon (tipo_secundario);

CREATE INDEX IF NOT EXISTS idx_pokemon_tipos ON pokemon (tipo_primario, tipo_secundario);
