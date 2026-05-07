package d1embed

import (
	"encoding/json"
	"fmt"
)

// Snapshot es el JSON embebido en el binario (exportado antes del build desde el SQLite de D1).
type Snapshot struct {
	Version             int                 `json:"version"`
	Organizers          []OrganizerRow      `json:"organizers"`
	Tournaments         []TournamentRow     `json:"tournaments"`
	TournamentPhases    []PhaseRow          `json:"tournament_phases"`
	TournamentStandings []StandingRow       `json:"tournament_standings"`
	Pokemon             []PokemonRow        `json:"pokemon"`
}

type OrganizerRow struct {
	ID   int     `json:"id"`
	Name *string `json:"name"`
}

type TournamentRow struct {
	ID           string  `json:"id"`
	Game         string  `json:"game"`
	Format       string  `json:"format"`
	Name         string  `json:"name"`
	Date         string  `json:"date"`
	Players      int     `json:"players"`
	OrganizerID  int     `json:"organizer_id"`
	Platform     *string `json:"platform"`
	Decklists    *int    `json:"decklists"`
	IsPublic     *int    `json:"is_public"`
	IsOnline     *int    `json:"is_online"`
}

type PhaseRow struct {
	TournamentID string `json:"tournament_id"`
	Phase        int    `json:"phase"`
	Type         string `json:"type"`
	Rounds       int    `json:"rounds"`
	Mode         string `json:"mode"`
}

type StandingRow struct {
	TournamentID string  `json:"tournament_id"`
	Placing      int     `json:"placing"`
	DisplayName  string  `json:"display_name"`
	PlayerHandle string  `json:"player_handle"`
	Country      *string `json:"country"`
	Wins         int     `json:"wins"`
	Losses       int     `json:"losses"`
	Ties         int     `json:"ties"`
	DropRound    *int    `json:"drop_round"`
	DeckJSON     string  `json:"deck_json"`
	DecklistJSON string  `json:"decklist_json"`
}

type PokemonRow struct {
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	TipoPrimario   string  `json:"tipo_primario"`
	TipoSecundario *string `json:"tipo_secundario"`
	SpriteURL      string  `json:"sprite_url"`
	UsageCount     int     `json:"usage_count"`
}

// ParseSnapshot tolera el campo _comment u otros desconocidos mediante Decoder UseNumber si hiciera falta — aquí struct strict.
func ParseSnapshot(raw []byte) (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// IsEmpty true si no hay nada que volcar (snapshot por defecto antes de export real).
func (s *Snapshot) IsEmpty() bool {
	if s == nil {
		return true
	}
	return len(s.Organizers) == 0 && len(s.Tournaments) == 0 && len(s.TournamentPhases) == 0 &&
		len(s.TournamentStandings) == 0 && len(s.Pokemon) == 0
}

// CountsLine resume filas para logs / dry-run.
func (s *Snapshot) CountsLine() string {
	if s == nil {
		return "snapshot=<nil>"
	}
	return fmt.Sprintf("organizers=%d tournaments=%d phases=%d standings=%d pokemon=%d",
		len(s.Organizers), len(s.Tournaments), len(s.TournamentPhases),
		len(s.TournamentStandings), len(s.Pokemon))
}
