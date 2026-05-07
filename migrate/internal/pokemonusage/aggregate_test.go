package pokemonusage

import (
	"encoding/json"
	"testing"

	"github.com/decimodan/vgchampions-api/migrate/internal/importer"
)

func TestAggregateDecklistCounts_filterAndCount(t *testing.T) {
	makePayload := func(game, format string, players int, decklist string) *importer.RedisPayload {
		d := map[string]any{
			"id":        "t1",
			"game":      game,
			"format":    format,
			"players":   players,
			"organizer": map[string]any{"id": 1},
			"phases":    []any{},
		}
		db, _ := json.Marshal(d)
		var st []map[string]json.RawMessage
		if decklist != "" {
			st = append(st, map[string]json.RawMessage{
				"decklist": json.RawMessage(decklist),
			})
		}
		sb, _ := json.Marshal(st)
		p, err := importer.ParseRedisPayload(string(db), string(sb))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	deckA := `[{"id":"a","name":"A"},{"id":"b","name":"B"}]`
	deckB := `[{"id":"a","name":"A"},{"id":"a","name":"A"}]`

	payloads := []*importer.RedisPayload{
		makePayload("VGC", "JUN", 20, deckA),  // wrong format
		makePayload("VGC", "M-A", 10, deckA), // players not > 10
		makePayload("VGC", "M-A", 11, deckA), // +2 a, +1 b
		makePayload("VGC", "M-A", 50, deckB), // +2 a (two decklist ids)
	}
	counts, sum := AggregateDecklistCounts(payloads, "VGC", "M-A", 10)
	if sum.TournamentsEligible != 2 {
		t.Fatalf("eligible=%d want 2", sum.TournamentsEligible)
	}
	if sum.StandingsScanned != 2 {
		t.Fatalf("standings=%d want 2", sum.StandingsScanned)
	}
	if counts["a"] != 3 || counts["b"] != 1 {
		t.Fatalf("counts a=%d b=%d (want 3,1)", counts["a"], counts["b"])
	}
	if sum.PokemonAppearances != 4 {
		t.Fatalf("appearances=%d want 4", sum.PokemonAppearances)
	}
}

func TestAggregateDecklistCounts_omitFiltersWhenEmpty(t *testing.T) {
	d := map[string]any{"id": "z", "game": "OTHER", "format": "XXX", "players": 20, "organizer": map[string]any{"id": 1}, "phases": []any{}}
	db, _ := json.Marshal(d)
	st := []map[string]json.RawMessage{{"decklist": json.RawMessage(`[{"id":"x"}]`)}}
	sb, _ := json.Marshal(st)
	p, err := importer.ParseRedisPayload(string(db), string(sb))
	if err != nil {
		t.Fatal(err)
	}
	counts, sum := AggregateDecklistCounts([]*importer.RedisPayload{p}, "", "", 19)
	if sum.TournamentsEligible != 1 || counts["x"] != 1 {
		t.Fatalf("%+v %+v", sum, counts)
	}
}
