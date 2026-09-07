// Package core holds the application logic: deciding whether a call is allowed,
// finding the tool, running it, and classifying what comes back. It depends on
// port and domain, and on nothing else.
package core

import (
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// ScopeAuthorizer allows an action when the principal holds the exact scope the
// action requires.
//
// It is a thin wrapper over domain.Principal.HasScope, and that is the point:
// authorization is a port, so the day this server needs role hierarchies,
// wildcard scopes or an external policy decision point, that arrives as a
// different Authorizer and nothing in the dispatch path changes.
type ScopeAuthorizer struct{}

// Allow reports whether p may perform an action requiring requiredScope.
// An empty requiredScope means the action needs no scope.
func (ScopeAuthorizer) Allow(p domain.Principal, requiredScope string) bool {
	return p.HasScope(requiredScope)
}

var _ port.Authorizer = ScopeAuthorizer{}
