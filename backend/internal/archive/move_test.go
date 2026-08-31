package archive

import (
	"os"
	"path/filepath"
	"testing"
)

// Recordings were once filed under an identifier the service generated, which a
// camera loses the moment it is removed and added back. These cover the move to
// the camera's own MAC, and in particular that it never destroys the copy the
// rest of the service can find.
func TestMoveCamera(t *testing.T) {
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		return string(b)
	}

	t.Run("moves what the destination does not have", func(t *testing.T) {
		root := t.TempDir()
		s, err := Open(root, 0)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, "oldid", "2026-08-30", "131529.avi"), "footage")
		write(t, filepath.Join(root, "oldid", "2026-08-30", "131529.json"), "{}")

		moved, err := s.MoveCamera("oldid", "a0b765554b84")
		if err != nil {
			t.Fatal(err)
		}
		if moved != 1 {
			t.Fatalf("moved %d recordings, want 1", moved)
		}
		if got := read(t, filepath.Join(root, "a0b765554b84", "2026-08-30", "131529.avi")); got != "footage" {
			t.Fatalf("recording arrived as %q", got)
		}
		if _, err := os.Stat(filepath.Join(root, "oldid")); !os.IsNotExist(err) {
			t.Fatal("the old directory is still there")
		}
	})

	t.Run("keeps the destination copy and drops the duplicate", func(t *testing.T) {
		root := t.TempDir()
		s, err := Open(root, 0)
		if err != nil {
			t.Fatal(err)
		}
		// The same recording under both keys, which is exactly what re-pulling
		// after a camera was re-added produces. The destination is the one the
		// rest of the service can find, so it is the one that must survive.
		write(t, filepath.Join(root, "oldid", "2026-08-30", "131529.avi"), "old copy")
		write(t, filepath.Join(root, "a0b765554b84", "2026-08-30", "131529.avi"), "kept copy")

		if _, err := s.MoveCamera("oldid", "a0b765554b84"); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(root, "a0b765554b84", "2026-08-30", "131529.avi")); got != "kept copy" {
			t.Fatalf("the destination copy was overwritten, now %q", got)
		}
		if _, err := os.Stat(filepath.Join(root, "oldid")); !os.IsNotExist(err) {
			t.Fatal("the duplicate was left on disk, so the move reclaimed nothing")
		}
	})

	t.Run("a transcoded destination still wins over an untranscoded source", func(t *testing.T) {
		root := t.TempDir()
		s, err := Open(root, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Transcoding may have run on one side only. The names differ, so both
		// survive the move; the reader prefers the MP4 and the AVI ages out.
		write(t, filepath.Join(root, "oldid", "2026-08-30", "131529.avi"), "avi")
		write(t, filepath.Join(root, "mac", "2026-08-30", "131529.mp4"), "mp4")

		if _, err := s.MoveCamera("oldid", "mac"); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(root, "mac", "2026-08-30", "131529.mp4")); got != "mp4" {
			t.Fatal("the transcoded copy was lost")
		}
	})

	t.Run("refuses a move that is not one", func(t *testing.T) {
		s, err := Open(t.TempDir(), 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ from, to string }{
			{"same", "same"},
			{"../escape", "mac"},
			{"oldid", "../escape"},
			{"", "mac"},
		} {
			if _, err := s.MoveCamera(c.from, c.to); err == nil {
				t.Fatalf("moving %q to %q was allowed", c.from, c.to)
			}
		}
	})

	t.Run("a camera with nothing held is not an error", func(t *testing.T) {
		s, err := Open(t.TempDir(), 0)
		if err != nil {
			t.Fatal(err)
		}
		moved, err := s.MoveCamera("neverwrote", "mac")
		if err != nil || moved != 0 {
			t.Fatalf("moved %d, err %v", moved, err)
		}
	})
}
