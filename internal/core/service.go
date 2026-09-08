package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

// ToolInfo is the listing view of a tool: what a client needs in order to call
// it, and nothing more. RequiredScope is deliberately absent — a client has no
// use for it, and publishing the scope map is free reconnaissance.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// CallOutcome is what came back from a tool that actually ran.
//
// The split between Failure here and the error returned alongside it is the
// two-channel error model: a *domain.ToolError is something the model should
// read and may retry against, so it travels in Failure with a nil error, while
// an infrastructure failure travels as an error and never reaches the model.
// Classifying in the core means every transport adapter maps the same way
// rather than inventing its own convention.
type CallOutcome struct {
	Result  port.Result
	Failure *domain.ToolError
}

// Service is the application core: authorize, find, run, classify.
type Service struct {
	registry *Registry
	authz    port.Authorizer
}

// NewService wires the core. Both arguments are required.
func NewService(registry *Registry, authz port.Authorizer) *Service {
	if registry == nil {
		panic("core: nil registry")
	}
	if authz == nil {
		panic("core: nil authorizer")
	}
	return &Service{registry: registry, authz: authz}
}

// ListTools returns the tools p is allowed to use, in registration order.
//
// Filtering the listing rather than only the call means a user never sees a
// tool they cannot run, which keeps the model from planning around a capability
// it will be refused.
func (s *Service) ListTools(p domain.Principal) []ToolInfo {
	tools := s.registry.All()
	infos := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		if !s.authz.Allow(p, t.RequiredScope()) {
			continue
		}
		infos = append(infos, ToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return infos
}

// AllTools returns every registered tool, unfiltered, in registration order.
//
// It exists for a transport that registers the whole tool set and filters what
// it advertises per request. Authorization is not skipped by using it: what a
// caller may actually run is still decided by CallTool.
func (s *Service) AllTools() []ToolInfo {
	tools := s.registry.All()
	infos := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		infos = append(infos, ToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return infos
}

// CallTool authorizes, resolves and runs a tool.
//
// It returns an error wrapping domain.ErrToolNotFound for an unknown name,
// domain.ErrForbidden when p lacks the tool's scope, and the tool's own error
// for an infrastructure failure. A tool-level failure is not an error here: it
// comes back in CallOutcome.Failure.
func (s *Service) CallTool(ctx context.Context, p domain.Principal, name string, args json.RawMessage) (CallOutcome, error) {
	tool, err := s.registry.Lookup(name)
	if err != nil {
		return CallOutcome{}, err
	}

	// Lookup runs before authorization, so a caller lacking the scope learns
	// that the tool exists and is told which scope it needs. That is the
	// deliberate trade: this is a developer tool where a clear "you lack
	// notes:write" beats a misleading "no such tool", and the tool set is not a
	// secret worth protecting by obscurity. A deployment that disagrees can swap
	// the two checks.
	if !s.authz.Allow(p, tool.RequiredScope()) {
		return CallOutcome{}, fmt.Errorf("%w: %q requires scope %q", domain.ErrForbidden, name, tool.RequiredScope())
	}

	result, err := tool.Execute(ctx, p, args)
	if err != nil {
		var toolErr *domain.ToolError
		if errors.As(err, &toolErr) {
			return CallOutcome{Failure: toolErr}, nil
		}
		return CallOutcome{}, fmt.Errorf("tool %q: %w", name, err)
	}
	return CallOutcome{Result: result}, nil
}
