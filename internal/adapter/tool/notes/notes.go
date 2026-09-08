// Package notes is the reference tool with a driven port behind it.
//
// Where echo depends on nothing, these two tools depend on port.NoteStore, and
// so demonstrate the shape a real tool takes: the tool holds a port, the
// storage behind it is chosen at wiring time, and the tool cannot tell which
// adapter it got.
package notes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Scopes for the two tools. They are separate so that a token can be issued
// with read access and no ability to write, which is what makes the
// authorization filtering visible in practice rather than only in tests.
const (
	ScopeRead  = "notes:read"
	ScopeWrite = "notes:write"
)

// maxBodyBytes caps a note so a single call cannot fill the store's file.
const maxBodyBytes = 4096

// AddArgs are the arguments to notes_add.
//
// There is deliberately no owner field. Ownership comes from the authenticated
// principal, so it cannot be supplied, spoofed or mistyped by the caller — the
// schema simply gives it nowhere to go.
type AddArgs struct {
	Body string `json:"body" jsonschema:"the text of the note"`
}

// ListArgs are the arguments to notes_list: none. A caller lists their own
// notes and nobody else's, so there is nothing to ask for.
type ListArgs struct{}

// All returns both notes tools, wired to store.
func All(store port.NoteStore) []port.Tool {
	return []port.Tool{NewAdd(store), NewList(store)}
}

// NewAdd returns the notes_add tool.
func NewAdd(store port.NoteStore) port.Tool {
	if store == nil {
		panic("notes: nil store")
	}
	return tool.Typed("notes_add", "Save a note for the calling user.", ScopeWrite,
		func(ctx context.Context, p domain.Principal, in AddArgs) (port.Result, error) {
			body := strings.TrimSpace(in.Body)
			if body == "" {
				return port.Result{}, domain.NewToolError("body must not be empty")
			}
			if len(body) > maxBodyBytes {
				return port.Result{}, domain.NewToolError(
					"body must be at most %d bytes, got %d", maxBodyBytes, len(body))
			}

			// The owner is the principal, never an argument.
			note, err := store.Add(ctx, p.Subject, body)
			if err != nil {
				// A storage failure is not something the model can fix, so it
				// stays an infrastructure error and never reaches it.
				return port.Result{}, fmt.Errorf("saving note for %q: %w", p.Subject, err)
			}

			return port.Result{
				Text:       "Saved note " + note.ID + ".",
				Structured: view(note),
			}, nil
		})
}

// NewList returns the notes_list tool.
func NewList(store port.NoteStore) port.Tool {
	if store == nil {
		panic("notes: nil store")
	}
	return tool.Typed("notes_list", "List the calling user's notes, oldest first.", ScopeRead,
		func(ctx context.Context, p domain.Principal, _ ListArgs) (port.Result, error) {
			notes, err := store.List(ctx, p.Subject)
			if err != nil {
				return port.Result{}, fmt.Errorf("listing notes for %q: %w", p.Subject, err)
			}

			views := make([]noteView, 0, len(notes))
			lines := make([]string, 0, len(notes))
			for _, n := range notes {
				views = append(views, view(n))
				lines = append(lines, fmt.Sprintf("%s  %s", n.ID, n.Body))
			}

			text := strings.Join(lines, "\n")
			if text == "" {
				// An empty list is a normal outcome, not a failure. Saying so in
				// words stops the model reading a blank response as an error.
				text = "No notes."
			}

			return port.Result{
				Text:       text,
				Structured: map[string]any{"notes": views},
			}, nil
		})
}

// noteView is the model-facing shape of a note. Owner is omitted: a caller only
// ever sees their own notes, so repeating their name adds nothing.
type noteView struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func view(n domain.Note) noteView {
	return noteView{ID: n.ID, Body: n.Body, CreatedAt: n.CreatedAt}
}
