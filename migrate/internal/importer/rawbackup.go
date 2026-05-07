package importer

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UpsertRedisRawSnapshots escribe JSON crudo en una transacción propia y la confirma antes del import tabular.
func UpsertRedisRawSnapshots(ctx context.Context, pool *pgxpool.Pool, payloads []*RedisPayload) error {
	if len(payloads) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	const q = `
INSERT INTO tournament_redis_raw (tournament_id, details_json, standings_json)
VALUES ($1, $2, $3)
ON CONFLICT (tournament_id) DO UPDATE SET
  details_json = EXCLUDED.details_json,
  standings_json = EXCLUDED.standings_json,
  updated_at = now()`

	for _, p := range payloads {
		tid := p.Details.ID
		var standings any
		if strings.TrimSpace(p.StandingsRawJSON) != "" {
			standings = p.StandingsRawJSON
		} else {
			standings = nil
		}
		if _, err := tx.Exec(ctx, q, tid, p.DetailsRawJSON, standings); err != nil {
			return fmt.Errorf("raw backup %s: %w", tid, err)
		}
	}
	return tx.Commit(ctx)
}

// AppendListJSONSnapshot guarda una copia del contenido de --input (no parseado).
func AppendListJSONSnapshot(ctx context.Context, pool *pgxpool.Pool, listJSON []byte) error {
	s := strings.TrimSpace(string(listJSON))
	if len(s) == 0 {
		return nil
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO tournaments_list_json_snapshots (list_json) VALUES ($1)`,
		s,
	)
	return err
}
