package pokemonusage

import (
	"testing"
)

func TestSortedUsageDescending(t *testing.T) {
	counts := map[string]int{"b": 2, "a": 3, "c": 2}
	got := SortedUsageDescending(counts)
	if len(got) != 3 {
		t.Fatal(len(got))
	}
	if got[0].Slug != "a" || got[0].Count != 3 {
		t.Fatalf("want first a 3 got %+v", got[0])
	}
	if got[1].Slug != "b" || got[1].Count != 2 || got[2].Slug != "c" || got[2].Count != 2 {
		t.Fatalf("tie-break alphabetical %+v %+v", got[1], got[2])
	}
}
