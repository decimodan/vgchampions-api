package d1embed

import (
	"encoding/json"
	"fmt"
)

type typeSlug struct {
	Slug string `json:"slug"`
}

// PokemonTiposDesdeLegacyTypesJSON convierte types_json (migración D1 antigua) a tipo primario/secundario.
func PokemonTiposDesdeLegacyTypesJSON(typesJSON string) (prim string, sec *string, err error) {
	var arr []typeSlug
	if err := json.Unmarshal([]byte(typesJSON), &arr); err != nil {
		return "", nil, err
	}
	if len(arr) == 0 || arr[0].Slug == "" {
		return "", nil, fmt.Errorf("types_json sin tipo primario")
	}
	prim = arr[0].Slug
	if len(arr) > 1 && arr[1].Slug != "" {
		s := arr[1].Slug
		sec = &s
	}
	return prim, sec, nil
}
