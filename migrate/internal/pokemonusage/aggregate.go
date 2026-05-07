package pokemonusage

import (
	"encoding/json"
	"strings"

	"github.com/decimodan/vgchampions-api/migrate/internal/importer"
)

// Summary describe el agregado (torneos que pasaron filtro standings considerados).
type Summary struct {
	TournamentsEligible int // details con game OK y players > umbral
	StandingsScanned    int // filas de standings en esos torneos
	PokemonAppearances  int // suma de elementos en todas las decklist (incluye repetición por jugador/torneo)
	UniqueSlugs         int // len(counts)
}

// AggregateDecklistCounts cuenta apariciones por slug (decklist[].id) en standings.
// Si gameFilter está vacío (tras trim), no filtra por game; igual para formatFilter.
// En los JSON de champions, game suele ser "VGC" y el Masters A va en format "M-A".
func AggregateDecklistCounts(payloads []*importer.RedisPayload, gameFilter, formatFilter string, minPlayersExclusive int) (counts map[string]int, summary Summary) {
	counts = make(map[string]int)
	gameFilter = strings.TrimSpace(gameFilter)
	formatFilter = strings.TrimSpace(formatFilter)
	if gameFilter == "-" {
		gameFilter = ""
	}
	if formatFilter == "-" {
		formatFilter = ""
	}
	for _, p := range payloads {
		if p == nil || p.Details == nil {
			continue
		}
		d := p.Details
		if gameFilter != "" && !strings.EqualFold(strings.TrimSpace(d.Game), gameFilter) {
			continue
		}
		if formatFilter != "" && !strings.EqualFold(strings.TrimSpace(d.Format), formatFilter) {
			continue
		}
		if d.Players <= minPlayersExclusive {
			continue
		}
		summary.TournamentsEligible++

		for _, row := range p.Standings {
			summary.StandingsScanned++
			n := appendDecklistCounts(row.Decklist, counts)
			summary.PokemonAppearances += n
		}
	}
	summary.UniqueSlugs = len(counts)
	return counts, summary
}

func appendDecklistCounts(raw json.RawMessage, counts map[string]int) int {
	if len(raw) == 0 {
		return 0
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "[]" {
		return 0
	}
	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0
	}
	added := 0
	for _, it := range items {
		slug := strings.TrimSpace(it.ID)
		if slug == "" {
			continue
		}
		counts[slug]++
		added++
	}
	return added
}
