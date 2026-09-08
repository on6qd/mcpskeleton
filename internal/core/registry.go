package core

import (
	"fmt"
	"slices"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// Registry holds the tools the server exposes.
//
// It is built once at wiring time and never mutated, which is why it needs no
// locking despite being read concurrently by every request. Tools are not added
// at runtime by design: adding a tool means writing an adapter and rebuilding,
// so there is no dynamic registration to keep safe.
type Registry struct {
	byName map[string]port.Tool
	order  []string
}

// NewRegistry builds a registry from the given tools, preserving their order for
// listing.
//
// A duplicate name, an empty name or a nil tool is a wiring mistake rather than
// a runtime condition, so it panics: the binary should not start with a
// misconfigured tool set, and the caller is a hardcoded list in the registry
// package, not user input.
func NewRegistry(tools ...port.Tool) *Registry {
	r := &Registry{
		byName: make(map[string]port.Tool, len(tools)),
		order:  make([]string, 0, len(tools)),
	}
	for i, t := range tools {
		if t == nil {
			panic(fmt.Sprintf("core: nil tool at index %d", i))
		}
		name := t.Name()
		if name == "" {
			panic(fmt.Sprintf("core: tool at index %d has an empty name", i))
		}
		if _, dup := r.byName[name]; dup {
			panic(fmt.Sprintf("core: duplicate tool name %q", name))
		}
		r.byName[name] = t
		r.order = append(r.order, name)
	}
	return r
}

// Lookup returns the tool registered under name, or an error wrapping
// domain.ErrToolNotFound.
func (r *Registry) Lookup(name string) (port.Tool, error) {
	t, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", domain.ErrToolNotFound, name)
	}
	return t, nil
}

// All returns every registered tool in registration order.
//
// The returned slice is a copy, so a caller cannot reorder or truncate the
// registry's own view.
func (r *Registry) All() []port.Tool {
	tools := make([]port.Tool, 0, len(r.order))
	for _, name := range r.order {
		tools = append(tools, r.byName[name])
	}
	return tools
}

// Names returns every registered tool name in registration order.
func (r *Registry) Names() []string {
	return slices.Clone(r.order)
}

// Scopes returns every distinct scope some registered tool requires, sorted.
//
// It exists for the protected-resource metadata document, which advertises the
// scopes a client may ask an authorization server for. Note that this is a set
// of scope names, not the tool-to-scope mapping: which tool needs which
// permission stays unpublished.
func (r *Registry) Scopes() []string {
	seen := make(map[string]bool, len(r.order))
	scopes := make([]string, 0, len(r.order))
	for _, name := range r.order {
		scope := r.byName[name].RequiredScope()
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		scopes = append(scopes, scope)
	}
	slices.Sort(scopes)
	return scopes
}
