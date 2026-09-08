package memory_test

import (
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/memory"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/notestest"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

func TestContract(t *testing.T) {
	t.Parallel()

	notestest.RunContract(t, func(*testing.T) port.NoteStore {
		return memory.New()
	})
}
