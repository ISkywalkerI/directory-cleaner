package cleaner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectSkipsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink unavailable")
	}
	files, total, failures := collect(root)
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %v", failures)
	}
	if len(files) != 1 || files[0].Path != target || total != int64(len("data")) {
		t.Fatalf("unexpected collection: files=%v total=%d", files, total)
	}
}
