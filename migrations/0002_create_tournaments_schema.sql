-- Migration number: 0002 	 2026-05-06T00:00:00.000Z
-- Schema for Pokémon Champions tournament data (sources: tournaments.json, details.json, standings.json)

CREATE TABLE IF NOT EXISTS organizers (
    id INTEGER PRIMARY KEY NOT NULL,
    name TEXT
);

CREATE TABLE IF NOT EXISTS tournaments (
    id TEXT PRIMARY KEY NOT NULL,
    game TEXT NOT NULL,
    format TEXT NOT NULL,
    name TEXT NOT NULL,
    date TEXT NOT NULL,
    players INTEGER NOT NULL,
    organizer_id INTEGER NOT NULL,
    platform TEXT,
    decklists INTEGER,
    is_public INTEGER,
    is_online INTEGER,
    FOREIGN KEY (organizer_id) REFERENCES organizers(id)
);

CREATE TABLE IF NOT EXISTS tournament_phases (
    tournament_id TEXT NOT NULL,
    phase INTEGER NOT NULL,
    type TEXT NOT NULL,
    rounds INTEGER NOT NULL,
    mode TEXT NOT NULL,
    PRIMARY KEY (tournament_id, phase),
    FOREIGN KEY (tournament_id) REFERENCES tournaments(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS tournament_standings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tournament_id TEXT NOT NULL,
    placing INTEGER NOT NULL,
    display_name TEXT NOT NULL,
    player_handle TEXT NOT NULL,
    country TEXT,
    wins INTEGER NOT NULL,
    losses INTEGER NOT NULL,
    ties INTEGER NOT NULL,
    drop_round INTEGER,
    deck_json TEXT NOT NULL DEFAULT '{}',
    decklist_json TEXT NOT NULL DEFAULT '[]',
    UNIQUE (tournament_id, player_handle),
    FOREIGN KEY (tournament_id) REFERENCES tournaments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tournaments_date ON tournaments(date DESC);
CREATE INDEX IF NOT EXISTS idx_tournaments_organizer ON tournaments(organizer_id);
CREATE INDEX IF NOT EXISTS idx_tournaments_game_format ON tournaments(game, format);
CREATE INDEX IF NOT EXISTS idx_phases_tournament ON tournament_phases(tournament_id);
CREATE INDEX IF NOT EXISTS idx_standings_tournament ON tournament_standings(tournament_id);
CREATE INDEX IF NOT EXISTS idx_standings_placing ON tournament_standings(tournament_id, placing);
