// Package gamedata holds the static configuration tables designed by game
// designers (items, ...). Tables are embedded into the binary so a server
// build always ships with consistent data.
package gamedata

import (
	"embed"

	"gameserver/framework/datatable"
)

//go:embed tables/*.json
var files embed.FS

// Item is one row of tables/items.json.
type Item struct {
	ID     int32  `json:"id"`
	Name   string `json:"name"`
	Usable bool   `json:"usable"`
	// Effects applied per unit when the item is used.
	Exp  int64 `json:"exp"`
	Gold int64 `json:"gold"`
}

// Tables groups every loaded table.
type Tables struct {
	Items *datatable.Table[int32, Item]
}

// Load parses every embedded table.
func Load() (*Tables, error) {
	items, err := datatable.LoadJSON(files, "tables/items.json", func(i *Item) int32 { return i.ID })
	if err != nil {
		return nil, err
	}
	return &Tables{Items: items}, nil
}
