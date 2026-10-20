// Package model defines persistent documents. Field tags are the MongoDB
// schema; keep names short and stable because renaming a tag is a data migration.
package model

import "time"

// Account is a login identity. The account name is the primary key.
type Account struct {
	Name         string    `bson:"_id"`
	PasswordHash []byte    `bson:"pwd"`
	UID          int64     `bson:"uid"`
	CreatedAt    time.Time `bson:"created_at"`
}

// ItemStack is a quantity of one item.
type ItemStack struct {
	ID    int32 `bson:"id"`
	Count int64 `bson:"n"`
}

// Player is the whole persistent state of one character, saved as one document.
type Player struct {
	UID         int64       `bson:"_id"`
	Name        string      `bson:"name"`
	Level       int32       `bson:"level"`
	Exp         int64       `bson:"exp"`
	Gold        int64       `bson:"gold"`
	Items       []ItemStack `bson:"items"`
	PosX        float32     `bson:"pos_x"`
	PosY        float32     `bson:"pos_y"`
	CreatedAt   time.Time   `bson:"created_at"`
	LastLoginAt time.Time   `bson:"last_login_at"`
	// Version increases on every save; useful for debugging lost updates.
	Version int64 `bson:"version"`
}

// Clone returns a deep copy (used by the in-memory repository and async saves).
func (p *Player) Clone() *Player {
	c := *p
	c.Items = append([]ItemStack(nil), p.Items...)
	return &c
}
