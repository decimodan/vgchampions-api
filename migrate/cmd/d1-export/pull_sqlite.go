package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/decimodan/vgchampions-api/migrate/internal/d1embed"
	_ "modernc.org/sqlite"
)

// PullFromSQLite construye el snapshot normalizado (solo lectura).
func PullFromSQLite(sqlitePath string) (*d1embed.Snapshot, error) {
	abs, err := resolveExistingSQLitePath(sqlitePath)
	if err != nil {
		return nil, err
	}
	uri := "file:" + filepath.ToSlash(abs) + "?mode=ro"
	sq, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer sq.Close()
	sq.SetMaxOpenConns(1)

	out := &d1embed.Snapshot{Version: 1}

	rows, err := sq.Query(`SELECT id, name FROM organizers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("organizers: %w", err)
	}
	for rows.Next() {
		var id int
		var name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		r := d1embed.OrganizerRow{ID: id}
		if name.Valid {
			s := name.String
			r.Name = &s
		}
		out.Organizers = append(out.Organizers, r)
	}
	_ = rows.Close()

	tr, err := sq.Query(`SELECT id, game, format, name, date, players, organizer_id, platform, decklists, is_public, is_online FROM tournaments ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("tournaments: %w", err)
	}
	for tr.Next() {
		var (
			id, game, format, name, date string
			players, orgID                 sql.NullInt64
			platform                       sql.NullString
			decklists, isPublic, isOnline  sql.NullInt64
		)
		if err := tr.Scan(&id, &game, &format, &name, &date, &players, &orgID, &platform, &decklists, &isPublic, &isOnline); err != nil {
			tr.Close()
			return nil, err
		}
		if !orgID.Valid {
			tr.Close()
			return nil, fmt.Errorf("tournament %q sin organizer_id", id)
		}
		r := d1embed.TournamentRow{
			ID:          id,
			Game:        game,
			Format:      format,
			Name:        name,
			Date:        date,
			Players:     intFromNull(players),
			OrganizerID: int(orgID.Int64),
		}
		if platform.Valid {
			s := platform.String
			r.Platform = &s
		}
		if decklists.Valid {
			v := int(decklists.Int64)
			r.Decklists = &v
		}
		if isPublic.Valid {
			v := int(isPublic.Int64)
			r.IsPublic = &v
		}
		if isOnline.Valid {
			v := int(isOnline.Int64)
			r.IsOnline = &v
		}
		out.Tournaments = append(out.Tournaments, r)
	}
	_ = tr.Close()

	pr, err := sq.Query(`SELECT tournament_id, phase, type, rounds, mode FROM tournament_phases ORDER BY tournament_id, phase`)
	if err != nil {
		return nil, fmt.Errorf("tournament_phases: %w", err)
	}
	for pr.Next() {
		var tid, typ, mode string
		var phase, rounds int
		if err := pr.Scan(&tid, &phase, &typ, &rounds, &mode); err != nil {
			pr.Close()
			return nil, err
		}
		out.TournamentPhases = append(out.TournamentPhases, d1embed.PhaseRow{
			TournamentID: tid, Phase: phase, Type: typ, Rounds: rounds, Mode: mode,
		})
	}
	_ = pr.Close()

	sr, err := sq.Query(`SELECT tournament_id, placing, display_name, player_handle, country, wins, losses, ties, drop_round, deck_json, decklist_json FROM tournament_standings ORDER BY tournament_id, placing`)
	if err != nil {
		return nil, fmt.Errorf("tournament_standings: %w", err)
	}
	for sr.Next() {
		var (
			tid, display, handle        string
			placing, wins, losses, ties int
			country                     sql.NullString
			drop                        sql.NullInt64
			deckNS, dlistNS             sql.NullString
		)
		if err := sr.Scan(&tid, &placing, &display, &handle, &country, &wins, &losses, &ties, &drop, &deckNS, &dlistNS); err != nil {
			sr.Close()
			return nil, err
		}
		st := d1embed.StandingRow{
			TournamentID: tid,
			Placing:      placing,
			DisplayName:  display,
			PlayerHandle: handle,
			Wins:         wins,
			Losses:       losses,
			Ties:         ties,
		}
		if country.Valid {
			s := country.String
			st.Country = &s
		}
		if drop.Valid {
			v := int(drop.Int64)
			st.DropRound = &v
		}
		if deckNS.Valid {
			st.DeckJSON = deckNS.String
		}
		if dlistNS.Valid {
			st.DecklistJSON = dlistNS.String
		}
		out.TournamentStandings = append(out.TournamentStandings, st)
	}
	_ = sr.Close()

	if hasTable(sq, "pokemon") {
		pokes, err := pullPokemon(sq)
		if err != nil {
			return nil, err
		}
		out.Pokemon = pokes
	}

	return out, nil
}

func pullPokemon(sq *sql.DB) ([]d1embed.PokemonRow, error) {
	tipoPrim, typesJSON, err := pokemonSQLiteSchema(sq)
	if err != nil {
		return nil, err
	}
	if tipoPrim {
		return pullPokemonV2(sq)
	}
	if typesJSON {
		return pullPokemonLegacy(sq)
	}
	return nil, fmt.Errorf("tabla pokemon sin tipo_primario ni types_json")
}

func pullPokemonV2(sq *sql.DB) ([]d1embed.PokemonRow, error) {
	rows, err := sq.Query(`SELECT slug, name, tipo_primario, tipo_secundario, sprite_url, usage_count FROM pokemon ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []d1embed.PokemonRow
	for rows.Next() {
		var slug, name, prim, sprite string
		var sec sql.NullString
		var usage sql.NullInt64
		if err := rows.Scan(&slug, &name, &prim, &sec, &sprite, &usage); err != nil {
			return nil, err
		}
		r := d1embed.PokemonRow{Slug: slug, Name: name, TipoPrimario: prim, SpriteURL: sprite, UsageCount: intFromNull(usage)}
		if sec.Valid {
			s := sec.String
			r.TipoSecundario = &s
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func pullPokemonLegacy(sq *sql.DB) ([]d1embed.PokemonRow, error) {
	rows, err := sq.Query(`SELECT slug, name, types_json, sprite_url, usage_count FROM pokemon ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []d1embed.PokemonRow
	for rows.Next() {
		var slug, name, typesJSON, sprite string
		var usage sql.NullInt64
		if err := rows.Scan(&slug, &name, &typesJSON, &sprite, &usage); err != nil {
			return nil, err
		}
		prim, secPtr, err := d1embed.PokemonTiposDesdeLegacyTypesJSON(typesJSON)
		if err != nil {
			return nil, fmt.Errorf("pokemon %s: %w", slug, err)
		}
		r := d1embed.PokemonRow{
			Slug:           slug,
			Name:           name,
			TipoPrimario:   prim,
			TipoSecundario: secPtr,
			SpriteURL:      sprite,
			UsageCount:     intFromNull(usage),
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func pokemonSQLiteSchema(sq *sql.DB) (tipoPrim, typesJSON bool, err error) {
	rows, err := sq.Query(`PRAGMA table_info(pokemon)`)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var colName, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &colName, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, false, err
		}
		switch strings.ToLower(colName) {
		case "tipo_primario":
			tipoPrim = true
		case "types_json":
			typesJSON = true
		}
	}
	return tipoPrim, typesJSON, rows.Err()
}

func hasTable(db *sql.DB, name string) bool {
	row := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND lower(name)=lower(?)`, name)
	var n int
	if row.Scan(&n) != nil {
		return false
	}
	return n > 0
}

func intFromNull(n sql.NullInt64) int {
	if !n.Valid {
		return 0
	}
	return int(n.Int64)
}

// resolveExistingSQLitePath evita el fallo típico: .env con ruta relativa a la raíz del repo
// pero ejecutar `cd migrate && go run ...` (sqlite en migrate/.wrangler no existe → SQLite errno 14).
func resolveExistingSQLitePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." {
		return "", fmt.Errorf("ruta SQLite vacía")
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	candidates := make([]string, 0, 4)
	add := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." {
			return
		}
		ap, er := filepath.Abs(p)
		if er != nil {
			return
		}
		for _, prev := range candidates {
			if prev == ap {
				return
			}
		}
		candidates = append(candidates, ap)
	}
	if filepath.IsAbs(raw) {
		add(raw)
	} else {
		add(filepath.Join(wd, raw))
		add(filepath.Join(wd, "..", raw))
	}
	lastErr := ""
	for _, p := range candidates {
		fi, statErr := os.Stat(p)
		if statErr == nil && fi != nil && !fi.IsDir() {
			return p, nil
		}
		if statErr != nil {
			lastErr = statErr.Error()
		} else if fi != nil && fi.IsDir() {
			lastErr = "es un directorio, falta el archivo .sqlite"
		}
	}
	msg := strings.Join(candidates, "\n        ")
	hint := ""
	if !filepath.IsAbs(raw) {
		hint = " Si en .env usas ruta relativa desde la raíz del repo y corres `cd migrate`, se prueba también el directorio padre."
	}
	detail := lastErr
	if detail == "" {
		detail = "archivo ausente"
	}
	return "", fmt.Errorf("no existe la base SQLite (detalle=%s;cwd=%q).%s\nRutas probadas por orden:\n        %s", detail, wd, hint, msg)
}
