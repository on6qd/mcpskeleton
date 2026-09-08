// Package registry is the one file you edit to add a tool.
//
// Adding a tool to this server is four steps, and this is the fourth:
//
//  1. Write a package under internal/adapter/tool/ with an arguments struct and
//     a function taking (ctx, domain.Principal, args).
//  2. Wrap it with tool.Typed, which derives its schema and validates its input.
//  3. If it needs anything from outside, declare a port for that and put the
//     real implementation behind an adapter.
//  4. Add it to the slice below.
//
// There is no dynamic registration and no plugin loader. Adding a tool means
// rebuilding, which is what keeps the tool set something the compiler checks.
package registry

import (
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/echo"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/tool/notes"
	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// New returns the registry of every tool this server exposes.
//
// Dependencies a tool needs arrive as arguments, so this function is where the
// choice of adapter is visible in one place.
func New(notesStore port.NoteStore) *core.Registry {
	tools := []port.Tool{
		echo.New(),
	}
	tools = append(tools, notes.All(notesStore)...)

	return core.NewRegistry(tools...)
}
