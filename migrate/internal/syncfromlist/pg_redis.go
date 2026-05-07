package syncfromlist

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decimodan/vgchampions-api/migrate/internal/redisx"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// LoadTournamentIDSet todos los ids en tabla tournaments.
func LoadTournamentIDSet(ctx context.Context, pool *pgxpool.Pool) (map[string]struct{}, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM tournaments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out, rows.Err()
}

// RedisHasDetailsAndStandings true si ambas claves existen y tienen valor no vacío.
func RedisHasDetailsAndStandings(ctx context.Context, rdb *redis.Client, prefix, id string) (bool, error) {
	kd := redisx.DetailsKey(prefix, id)
	ks := redisx.StandingsKey(prefix, id)
	ex, err := rdb.Exists(ctx, kd, ks).Result()
	if err != nil || ex < 2 {
		return false, err
	}
	detail, err := rdb.Get(ctx, kd).Result()
	if err != nil || strings.TrimSpace(detail) == "" {
		return false, err
	}
	st, err := rdb.Get(ctx, ks).Result()
	if err != nil || strings.TrimSpace(st) == "" {
		return false, err
	}
	return true, nil
}

// RawBackupTexts devuelve details/standings desde tournament_redis_raw.
func RawBackupTexts(ctx context.Context, pool *pgxpool.Pool, id string) (details string, standings string, ok bool, err error) {
	const q = `SELECT details_json, standings_json FROM tournament_redis_raw WHERE tournament_id = $1`
	var det, st *string
	if er := pool.QueryRow(ctx, q, id).Scan(&det, &st); er != nil {
		if errors.Is(er, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, er
	}
	if det == nil || strings.TrimSpace(*det) == "" {
		return "", "", false, nil
	}
	details = strings.TrimRight(strings.TrimSpace(*det), "\n")
	if st != nil && strings.TrimSpace(*st) != "" {
		standings = strings.TrimRight(strings.TrimSpace(*st), "\n")
	} else {
		standings = "[]"
	}
	return details, standings, true, nil
}

type synthDetail struct {
	ID        string            `json:"id"`
	Game      string            `json:"game"`
	Format    string            `json:"format"`
	Name      string            `json:"name"`
	Date      string            `json:"date"`
	Players   int               `json:"players"`
	Platform  *string           `json:"platform,omitempty"`
	Decklists *bool             `json:"decklists,omitempty"`
	IsPublic  *bool             `json:"isPublic,omitempty"`
	IsOnline  *bool             `json:"isOnline,omitempty"`
	Organizer synthOrganizer    `json:"organizer"`
	Phases    []json.RawMessage `json:"phases"`
}

type synthOrganizer struct {
	ID   int     `json:"id"`
	Name *string `json:"name,omitempty"`
}

func nullInt64Bool(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 != 0
	return &v
}

// SyntheticDetailsStandings arma details desde tournaments+organizers y standings "[]".
func SyntheticDetailsStandings(ctx context.Context, pool *pgxpool.Pool, id string) (detailsJSON, standingsJSON string, err error) {
	const q = `
SELECT t.game, t.format, t.name, t.date, t.players, t.organizer_id,
  t.platform, t.decklists, t.is_public, t.is_online, o.name
FROM tournaments t
LEFT JOIN organizers o ON o.id = t.organizer_id
WHERE t.id = $1`
	var game, format, name, date string
	var players, orgID int
	var plat sql.NullString
	var decklists, pub, online sql.NullInt64
	var orgName sql.NullString
	err = pool.QueryRow(ctx, q, id).Scan(
		&game, &format, &name, &date, &players, &orgID,
		&plat, &decklists, &pub, &online, &orgName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("sin fila tournaments para %s", id)
		}
		return "", "", err
	}
	var platPtr *string
	if plat.Valid {
		s := plat.String
		platPtr = &s
	}
	var orgPtr *string
	if orgName.Valid {
		s := orgName.String
		orgPtr = &s
	}
	d := synthDetail{
		ID:        id,
		Game:      game,
		Format:    format,
		Name:      name,
		Date:      date,
		Players:   players,
		Platform:  platPtr,
		Decklists: nullInt64Bool(decklists),
		IsPublic:  nullInt64Bool(pub),
		IsOnline:  nullInt64Bool(online),
		Organizer: synthOrganizer{ID: orgID, Name: orgPtr},
		Phases:    []json.RawMessage{},
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", "", err
	}
	return strings.TrimRight(strings.TrimSpace(string(b)), "\n"), `[]`, nil
}

func redisTTL(ttlSec int) time.Duration {
	if ttlSec <= 0 {
		return 0
	}
	return time.Duration(ttlSec) * time.Second
}

// WriteRedisTournamentKV escribe :details y :standings.
func WriteRedisTournamentKV(ctx context.Context, rdb *redis.Client, prefix string, id, detailsJSON, standingsJSON string, ttlSec int) error {
	kd := redisx.DetailsKey(prefix, id)
	ks := redisx.StandingsKey(prefix, id)
	ttl := redisTTL(ttlSec)
	detail := strings.TrimRight(strings.TrimSpace(detailsJSON), "\n")
	if detail == "" {
		return fmt.Errorf("details vacío para id %s", id)
	}
	st := standingsJSON
	if strings.TrimSpace(st) == "" {
		st = "[]"
	}
	st = strings.TrimRight(strings.TrimSpace(st), "\n")
	if err := rdb.Set(ctx, kd, detail, ttl).Err(); err != nil {
		return fmt.Errorf("SET details %s: %w", id, err)
	}
	if err := rdb.Set(ctx, ks, st, ttl).Err(); err != nil {
		return fmt.Errorf("SET standings %s: %w", id, err)
	}
	return nil
}
