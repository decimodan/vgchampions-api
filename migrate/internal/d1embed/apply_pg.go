package d1embed

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyEmbedded vuelca SnapshotData (embebido en el build) a PostgreSQL dentro de una sola transacción.
func ApplyEmbedded(ctx context.Context, pool *pgxpool.Pool) error {
	return ApplySnapshotBytes(ctx, pool, SnapshotData)
}

// ApplySnapshotBytes para pruebas / dry-run usando el mismo JSON que el embed.
func ApplySnapshotBytes(ctx context.Context, pool *pgxpool.Pool, raw []byte) error {
	s, err := ParseSnapshot(raw)
	if err != nil {
		return fmt.Errorf("parse snapshot JSON: %w", err)
	}
	if s.IsEmpty() {
		return nil
	}
	log.Printf("volcando snapshot embebido (%s)…", s.CountsLine())
	return applySnapshot(ctx, pool, s)
}

func applySnapshot(ctx context.Context, pool *pgxpool.Pool, s *Snapshot) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	orgQ := `INSERT INTO organizers (id, name) VALUES ($1, $2)
ON CONFLICT (id) DO UPDATE SET name = COALESCE(EXCLUDED.name, organizers.name)`
	for _, o := range s.Organizers {
		var name any
		if o.Name != nil {
			name = *o.Name
		}
		if _, err := tx.Exec(ctx, orgQ, o.ID, name); err != nil {
			return fmt.Errorf("organizer %d: %w", o.ID, err)
		}
	}

	const tq = `
INSERT INTO tournaments (id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
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
  is_online = EXCLUDED.is_online`

	tournIDs := make([]string, 0, len(s.Tournaments))
	for _, t := range s.Tournaments {
		var plat any
		if t.Platform != nil {
			plat = *t.Platform
		}
		if _, err := tx.Exec(ctx, tq, t.ID, t.Game, t.Format, t.Name, t.Date, t.Players,
			t.OrganizerID, plat,
			optionalInt(t.Decklists), optionalInt(t.IsPublic), optionalInt(t.IsOnline)); err != nil {
			return fmt.Errorf("tournament %s: %w", t.ID, err)
		}
		tournIDs = append(tournIDs, t.ID)
	}

	for _, tid := range tournIDs {
		if _, err := tx.Exec(ctx, `DELETE FROM tournament_phases WHERE tournament_id = $1`, tid); err != nil {
			return err
		}
	}
	const phInsert = `INSERT INTO tournament_phases (tournament_id, phase, type, rounds, mode) VALUES ($1,$2,$3,$4,$5)`
	for _, ph := range s.TournamentPhases {
		if _, err := tx.Exec(ctx, phInsert, ph.TournamentID, ph.Phase, ph.Type, ph.Rounds, ph.Mode); err != nil {
			return fmt.Errorf("phase %s/%d: %w", ph.TournamentID, ph.Phase, err)
		}
	}

	for _, tid := range tournIDs {
		if _, err := tx.Exec(ctx, `DELETE FROM tournament_standings WHERE tournament_id = $1`, tid); err != nil {
			return err
		}
	}
	const stInsert = `INSERT INTO tournament_standings (tournament_id, "placing", display_name, player_handle, country, wins, losses, ties, drop_round, deck_json, decklist_json)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

	for _, st := range s.TournamentStandings {
		var country any
		if st.Country != nil {
			country = *st.Country
		}
		var drop any
		if st.DropRound != nil {
			drop = *st.DropRound
		}
		deck := st.DeckJSON
		if deck == "" {
			deck = "{}"
		}
		dlist := st.DecklistJSON
		if dlist == "" {
			dlist = "[]"
		}
		if _, err := tx.Exec(ctx, stInsert,
			st.TournamentID, st.Placing, st.DisplayName, st.PlayerHandle,
			country, st.Wins, st.Losses, st.Ties, drop, deck, dlist); err != nil {
			return fmt.Errorf("standing %s/%d: %w", st.TournamentID, st.Placing, err)
		}
	}

	// usage_count: no rebajar totales ya subidos (p. ej. --update-pokemon-usage-from-decklists).
	const pokeQ = `
INSERT INTO pokemon (slug, name, tipo_primario, tipo_secundario, sprite_url, usage_count)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (slug) DO UPDATE SET
  name = EXCLUDED.name,
  tipo_primario = EXCLUDED.tipo_primario,
  tipo_secundario = EXCLUDED.tipo_secundario,
  sprite_url = EXCLUDED.sprite_url,
  usage_count = GREATEST(pokemon.usage_count, EXCLUDED.usage_count)`

	for _, p := range s.Pokemon {
		var sec any
		if p.TipoSecundario != nil {
			sec = *p.TipoSecundario
		}
		if _, err := tx.Exec(ctx, pokeQ, p.Slug, p.Name, p.TipoPrimario, sec, p.SpriteURL, p.UsageCount); err != nil {
			return fmt.Errorf("pokemon %s: %w", p.Slug, err)
		}
	}

	return tx.Commit(ctx)
}

func optionalInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// DryRunCounts devuelve métricas desde bytes (mismo JSON que embed).
func DryRunCounts(raw []byte) (string, error) {
	s, err := ParseSnapshot(raw)
	if err != nil {
		return "", err
	}
	if s.IsEmpty() {
		return "snapshot embebido vacío (normal: Postgres/Redis; opcionalmente d1-export antes de build)", nil
	}
	return s.CountsLine(), nil
}
