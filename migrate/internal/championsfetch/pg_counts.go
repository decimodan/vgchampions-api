package championsfetch

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CountIDsMissingFromTournaments devuelve cuántos IDs de want no están en la tabla tournaments.
func CountIDsMissingFromTournaments(ctx context.Context, pool *pgxpool.Pool, want []string) (missing int, err error) {
	if len(want) == 0 {
		return 0, nil
	}
	rows, err := pool.Query(ctx, `SELECT id FROM tournaments WHERE id = ANY($1::text[])`, want)
	if err != nil {
		return 0, fmt.Errorf("pg lista torneos: %w", err)
	}
	defer rows.Close()

	have := make(map[string]struct{}, len(want))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		have[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range want {
		if _, ok := have[id]; !ok {
			missing++
		}
	}
	return missing, nil
}
