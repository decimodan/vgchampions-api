package pokemonusage

import "slices"

// SlugCount es un slug con su número de apariciones en decklists agregadas.
type SlugCount struct {
	Slug  string
	Count int
}

// SortedUsageDescending ordena por apariciones (mayor primero), luego por slug lexicográfico.
func SortedUsageDescending(counts map[string]int) []SlugCount {
	out := make([]SlugCount, 0, len(counts))
	for s, n := range counts {
		out = append(out, SlugCount{Slug: s, Count: n})
	}
	slices.SortFunc(out, func(a, b SlugCount) int {
		if b.Count != a.Count {
			return b.Count - a.Count
		}
		switch {
		case a.Slug < b.Slug:
			return -1
		case a.Slug > b.Slug:
			return 1
		default:
			return 0
		}
	})
	return out
}
