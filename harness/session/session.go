// Package session defines durable session and turn identity.
package session

import "time"

type ID string

type TurnID string

type TurnType string

const (
	TurnRegular    TurnType = "regular"
	TurnCompaction TurnType = "compaction"
)

type Session struct {
	ID        ID
	CreatedAt time.Time
	Name      string `json:",omitempty"`
}

type Turn struct {
	ID             TurnID
	PreviousTurnID TurnID
	Type           TurnType
}
