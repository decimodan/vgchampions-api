package importer

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DedupeTournamentListByID conserva la última fila por id (útil si tournaments.json repite slugs).
func DedupeTournamentListByID(list []TournamentListJSONRow) []TournamentListJSONRow {
	if len(list) == 0 {
		return nil
	}
	last := make(map[string]TournamentListJSONRow)
	order := make([]string, 0)
	for _, r := range list {
		id := strings.TrimSpace(r.ID)
		if id == "" {
			continue
		}
		if _, ok := last[id]; !ok {
			order = append(order, id)
		}
		last[id] = r
	}
	out := make([]TournamentListJSONRow, 0, len(order))
	for _, id := range order {
		out = append(out, last[id])
	}
	return out
}

// OrganizerIDsFromList devuelve organizerId únicos (orden estable).
func OrganizerIDsFromList(list []TournamentListJSONRow) []int {
	seen := make(map[int]struct{})
	var out []int
	for _, r := range list {
		if _, ok := seen[r.OrganizerID]; ok {
			continue
		}
		seen[r.OrganizerID] = struct{}{}
		out = append(out, r.OrganizerID)
	}
	return out
}

// UpsertOrganizersAndTournamentsFromList INSERT/UPDATE tabla tournaments desde la lista JSON
// (solo columnas lista; organizers con id sólo donde falte nombre).
func UpsertOrganizersAndTournamentsFromList(ctx context.Context, pool *pgxpool.Pool, list []TournamentListJSONRow) error {
	if len(list) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	orgs := OrganizerIDsFromList(list)
	if err := insertOrganizers(ctx, tx, orgs); err != nil {
		return fmt.Errorf("organizers: %w", err)
	}
	if err := bulkUpsertFromList(ctx, tx, list); err != nil {
		return fmt.Errorf("tournaments: %w", err)
	}
	return tx.Commit(ctx)
}
