package core_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/core"
	"github.com/bartdelepeleer/mcpskeleton/internal/domain"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

func TestRegistryLookup(t *testing.T) {
	t.Parallel()

	echo := newFakeTool("echo", "tools:echo")
	notes := newFakeTool("notes_add", "notes:write")
	r := core.NewRegistry(echo, notes)

	got, err := r.Lookup("echo")
	if err != nil {
		t.Fatalf("Lookup(echo) error = %v", err)
	}
	if got != port.Tool(echo) {
		t.Errorf("Lookup(echo) returned a different tool than was registered")
	}
}

func TestRegistryLookupUnknown(t *testing.T) {
	t.Parallel()

	r := core.NewRegistry(newFakeTool("echo", ""))

	_, err := r.Lookup("nope")
	if !errors.Is(err, domain.ErrToolNotFound) {
		t.Fatalf("Lookup(nope) error = %v, want one wrapping ErrToolNotFound", err)
	}
	// The name belongs in the message; an operator reading a log should not have
	// to guess which tool was missing.
	if got := err.Error(); got == "" || !contains(got, "nope") {
		t.Errorf("error %q does not name the missing tool", got)
	}
}

func TestRegistryPreservesOrder(t *testing.T) {
	t.Parallel()

	r := core.NewRegistry(
		newFakeTool("c", ""),
		newFakeTool("a", ""),
		newFakeTool("b", ""),
	)

	want := []string{"c", "a", "b"}
	if got := r.Names(); !equal(got, want) {
		t.Errorf("Names() = %v, want %v (registration order, not sorted)", got, want)
	}

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("All() returned %d tools, want 3", len(all))
	}
	for i, name := range want {
		if all[i].Name() != name {
			t.Errorf("All()[%d] = %q, want %q", i, all[i].Name(), name)
		}
	}
}

// A caller must not be able to reach into the registry's own state through a
// slice it handed out.
func TestRegistryAllReturnsACopy(t *testing.T) {
	t.Parallel()

	r := core.NewRegistry(newFakeTool("a", ""), newFakeTool("b", ""))

	first := r.All()
	first[0] = nil
	names := r.Names()
	names[0] = "clobbered"

	if got := r.All(); got[0] == nil || got[0].Name() != "a" {
		t.Error("mutating a returned slice changed the registry")
	}
	if got := r.Names(); got[0] != "a" {
		t.Error("mutating returned names changed the registry")
	}
}

func TestRegistryEmpty(t *testing.T) {
	t.Parallel()

	r := core.NewRegistry()

	if got := r.All(); len(got) != 0 {
		t.Errorf("All() on an empty registry = %v, want empty", got)
	}
	if _, err := r.Lookup("anything"); !errors.Is(err, domain.ErrToolNotFound) {
		t.Errorf("Lookup on an empty registry error = %v, want ErrToolNotFound", err)
	}
}

// Misconfiguration is a wiring mistake, not a runtime condition: the binary
// should refuse to start rather than serve a broken tool set.
func TestRegistryPanicsOnWiringMistakes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tools []port.Tool
	}{
		{"duplicate name", []port.Tool{newFakeTool("echo", ""), newFakeTool("echo", "")}},
		{"empty name", []port.Tool{newFakeTool("", "")}},
		{"nil tool", []port.Tool{nil}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("NewRegistry(%s) did not panic", tc.name)
				}
			}()
			core.NewRegistry(tc.tools...)
		})
	}
}

// The registry is built once and read by every request. It carries no lock, so
// its immutability is the thing keeping it safe; this fails under -race if that
// ever stops being true.
func TestRegistryConcurrentReads(t *testing.T) {
	t.Parallel()

	r := core.NewRegistry(newFakeTool("a", ""), newFakeTool("b", ""))

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Lookup("a"); err != nil {
				t.Errorf("Lookup(a) error = %v", err)
			}
			_ = r.All()
			_ = r.Names()
		}()
	}
	wg.Wait()
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
