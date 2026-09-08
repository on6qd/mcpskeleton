package file_test

import (
	"path/filepath"
	"testing"

	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/file"
	"github.com/bartdelepeleer/mcpskeleton/internal/adapter/out/notes/notestest"
	"github.com/bartdelepeleer/mcpskeleton/internal/port"
)

func TestContract(t *testing.T) {
	t.Parallel()

	notestest.RunContract(t, func(t *testing.T) port.NoteStore {
		s, err := file.New(filepath.Join(t.TempDir(), "notes.json"))
		if err != nil {
			t.Fatalf("file.New error = %v", err)
		}
		return s
	})
}
