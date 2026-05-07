package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	tournamentBulk      = 40
	standingRowsPerBulk = 30
)

// TournamentListJSONRow matches docs/tournaments.json items.
type TournamentListJSONRow struct {
	ID          string `json:"id"`
	Game        string `json:"game"`
	Format      string `json:"format"`
	Name        string `json:"name"`
	Date        string `json:"date"`
	Players     int    `json:"players"`
	OrganizerID int    `json:"organizerId"`
}

type detailsJSON struct {
	ID        string  `json:"id"`
	Game      string  `json:"game"`
	Format    string  `json:"format"`
	Name      string  `json:"name"`
	Date      string  `json:"date"`
	Players   int     `json:"players"`
	Platform  *string `json:"platform"`
	Decklists *bool   `json:"decklists"`
	IsPublic  *bool   `json:"isPublic"`
	IsOnline  *bool   `json:"isOnline"`
	Organizer *struct {
		ID   int     `json:"id"`
		Name *string `json:"name"`
	} `json:"organizer"`
	Phases []struct {
		Phase  int    `json:"phase"`
		Type   string `json:"type"`
		Rounds int    `json:"rounds"`
		Mode   string `json:"mode"`
	} `json:"phases"`
}

type standingJSON struct {
	Placing  int              `json:"placing"`
	Name     string           `json:"name"`
	Player   string           `json:"player"`
	Country  *string          `json:"country"`
	Record   *standingRecordJ `json:"record"`
	Drop     *float64         `json:"drop"`
	Deck     json.RawMessage  `json:"deck"`
	Decklist json.RawMessage  `json:"decklist"`
}

type standingRecordJ struct {
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
	Ties   int `json:"ties"`
}

// RedisPayload is parsed tournament data from Redis keys (más texto crudo para respaldo).
type RedisPayload struct {
	Details            *detailsJSON
	Standings          []standingJSON
	DetailsRawJSON     string
	StandingsRawJSON   string
}

// ParseRedisPayload decodes JSON strings from Redis (details + optional standings).
func ParseRedisPayload(detailsJSONStr, standingsJSONStr string) (*RedisPayload, error) {
	var d detailsJSON
	if err := json.Unmarshal([]byte(detailsJSONStr), &d); err != nil {
		return nil, err
	}
	if d.ID == "" {
		return nil, fmt.Errorf("details sin id")
	}
	if d.Organizer == nil {
		return nil, fmt.Errorf("organizer ausente")
	}
	var standings []standingJSON
	if strings.TrimSpace(standingsJSONStr) != "" {
		if err := json.Unmarshal([]byte(standingsJSONStr), &standings); err != nil {
			return nil, fmt.Errorf("standings JSON: %w", err)
		}
	}
	return &RedisPayload{
		Details:            &d,
		Standings:          standings,
		DetailsRawJSON:     detailsJSONStr,
		StandingsRawJSON:   standingsJSONStr,
	}, nil
}

// ParseTournamentsList parses optional JSON array for --input.
func ParseTournamentsList(raw []byte) ([]TournamentListJSONRow, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	var rows []TournamentListJSONRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Import runs one transaction: optional list + merge de cada payload Redis (upsert; fases solo si el JSON trae fases).
func Import(ctx context.Context, pool *pgxpool.Pool, list []TournamentListJSONRow, payloads []*RedisPayload) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	orgIDs := collectOrganizers(list, payloads)
	if err := insertOrganizers(ctx, tx, orgIDs); err != nil {
		return err
	}
	if err := bulkUpsertFromList(ctx, tx, list); err != nil {
		return err
	}
	for _, p := range payloads {
		if err := mergeOne(ctx, tx, p); err != nil {
			return fmt.Errorf("torneo %s: %w", p.Details.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func collectOrganizers(list []TournamentListJSONRow, payloads []*RedisPayload) []int {
	seen := make(map[int]struct{})
	var ids []int
	for _, r := range list {
		v := r.OrganizerID
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		ids = append(ids, v)
	}
	for _, p := range payloads {
		v := p.Details.Organizer.ID
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		ids = append(ids, v)
	}
	return ids
}

func insertOrganizers(ctx context.Context, tx pgx.Tx, ids []int) error {
	for _, id := range ids {
		_, err := tx.Exec(ctx, `INSERT INTO organizers (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, id)
		if err != nil {
			return fmt.Errorf("insert organizer %d: %w", id, err)
		}
	}
	return nil
}

func bulkUpsertFromList(ctx context.Context, tx pgx.Tx, list []TournamentListJSONRow) error {
	for start := 0; start < len(list); start += tournamentBulk {
		end := min(start+tournamentBulk, len(list))
		if err := upsertListChunk(ctx, tx, list[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func upsertListChunk(ctx context.Context, tx pgx.Tx, chunk []TournamentListJSONRow) error {
	if len(chunk) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO tournaments (id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online) VALUES `)
	args := make([]interface{}, 0, len(chunk)*11)
	n := 1
	for i, t := range chunk {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			n, n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10)
		args = append(args, t.ID, t.Game, t.Format, t.Name, t.Date, t.Players, t.OrganizerID, nil, nil, nil, nil)
		n += 11
	}
	// La lista JSON no trae platform/flags: no machacar columnas ya rellenadas por Redis/snapshot.
	sb.WriteString(` ON CONFLICT (id) DO UPDATE SET
  game = EXCLUDED.game,
  format = EXCLUDED.format,
  name = EXCLUDED.name,
  date = EXCLUDED.date,
  players = EXCLUDED.players,
  organizer_id = EXCLUDED.organizer_id,
  platform = COALESCE(EXCLUDED.platform, tournaments.platform),
  decklists = COALESCE(EXCLUDED.decklists, tournaments.decklists),
  is_public = COALESCE(EXCLUDED.is_public, tournaments.is_public),
  is_online = COALESCE(EXCLUDED.is_online, tournaments.is_online)`)
	_, err := tx.Exec(ctx, sb.String(), args...)
	return err
}

func mergeOne(ctx context.Context, tx pgx.Tx, p *RedisPayload) error {
	d := p.Details
	oid := d.Organizer.ID
	var nameAny any
	if d.Organizer.Name != nil {
		nameAny = *d.Organizer.Name
	}

	_, err := tx.Exec(ctx, `
INSERT INTO organizers (id, name) VALUES ($1, $2)
ON CONFLICT (id) DO UPDATE SET name = COALESCE(EXCLUDED.name, organizers.name)`,
		oid, nameAny)
	if err != nil {
		return fmt.Errorf("upsert organizer: %w", err)
	}

	_, err = tx.Exec(ctx, `
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
  is_online = EXCLUDED.is_online`,
		d.ID, d.Game, d.Format, d.Name, d.Date, d.Players, oid, d.Platform,
		bool01(d.Decklists), bool01(d.IsPublic), bool01(d.IsOnline),
	)
	if err != nil {
		return fmt.Errorf("upsert tournament: %w", err)
	}

	if len(d.Phases) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM tournament_phases WHERE tournament_id = $1`, d.ID); err != nil {
			return err
		}
		for _, ph := range d.Phases {
			_, err := tx.Exec(ctx, `
INSERT INTO tournament_phases (tournament_id, phase, type, rounds, mode) VALUES ($1,$2,$3,$4,$5)`,
				d.ID, ph.Phase, ph.Type, ph.Rounds, ph.Mode)
			if err != nil {
				return fmt.Errorf("phase %d: %w", ph.Phase, err)
			}
		}
	}

	if len(p.Standings) == 0 {
		log.Printf("%s: sin standings en Redis (solo torneo/fases)", d.ID)
		return nil
	}

	if _, err := tx.Exec(ctx, `DELETE FROM tournament_standings WHERE tournament_id = $1`, d.ID); err != nil {
		return err
	}
	for start := 0; start < len(p.Standings); start += standingRowsPerBulk {
		end := min(start+standingRowsPerBulk, len(p.Standings))
		if err := insertStandingsChunk(ctx, tx, d.ID, p.Standings[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func insertStandingsChunk(ctx context.Context, tx pgx.Tx, tournamentID string, chunk []standingJSON) error {
	if len(chunk) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`
INSERT INTO tournament_standings (
  tournament_id, "placing", display_name, player_handle, country, wins, losses, ties, drop_round, deck_json, decklist_json
) VALUES `)
	args := make([]interface{}, 0, len(chunk)*11)
	n := 1
	for i, row := range chunk {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			n, n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10)
		wins, losses, ties := 0, 0, 0
		if row.Record != nil {
			wins = row.Record.Wins
			losses = row.Record.Losses
			ties = row.Record.Ties
		}
		var drop interface{}
		if row.Drop != nil {
			drop = int(*row.Drop + 1e-9) // valores enteros típicos
		}

		deck := normJSONArray(row.Deck, "{}")
		dlist := normJSONArray(row.Decklist, "[]")

		var countryAny any
		if row.Country != nil {
			countryAny = *row.Country
		}

		args = append(args, tournamentID, row.Placing, row.Name, row.Player, countryAny, wins, losses, ties, drop, deck, dlist)
		n += 11
	}
	_, err := tx.Exec(ctx, sb.String(), args...)
	return err
}

func normJSONArray(raw json.RawMessage, empty string) string {
	if len(raw) == 0 {
		return empty
	}
	return string(raw)
}

func bool01(b *bool) int {
	if b != nil && *b {
		return 1
	}
	return 0
}

