package domain

import "time"

// Note is the record behind the reference notes tool. It exists to give the
// skeleton one tool with a driven port and real state, so the adapter-swapping
// pattern is demonstrated and not merely described.
type Note struct {
	ID string

	// Owner is the Subject of the principal that created the note. Ownership
	// always comes from the authenticated identity, never from tool arguments.
	Owner string

	Body      string
	CreatedAt time.Time
}
