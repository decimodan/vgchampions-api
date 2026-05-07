-- Respaldo de datos crudos desde Redis (y opcional lista --input) para poder reintentar o auditar si falla el import tabular.

CREATE TABLE IF NOT EXISTS tournament_redis_raw (
    tournament_id TEXT PRIMARY KEY NOT NULL,
    details_json TEXT NOT NULL,
    standings_json TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE tournament_redis_raw IS 'Snapshots JSON desde Redis antes de cada import (details + standings).';


CREATE INDEX IF NOT EXISTS idx_tournament_redis_raw_updated ON tournament_redis_raw (updated_at DESC);

CREATE TABLE IF NOT EXISTS tournaments_list_json_snapshots (
    id BIGSERIAL PRIMARY KEY,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    list_json TEXT NOT NULL
);

COMMENT ON TABLE tournaments_list_json_snapshots IS 'Copia del JSON pasado con --input (lista de torneos) en cada ejecución.';

CREATE INDEX IF NOT EXISTS idx_tournaments_list_snapshots_cap ON tournaments_list_json_snapshots (captured_at DESC);
