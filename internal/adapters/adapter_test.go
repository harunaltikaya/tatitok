package adapters

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func matchJSONLForTest(path string) string {
	if strings.HasSuffix(path, ".jsonl") {
		return path
	}
	return ""
}

// A root that is itself a symlink to a directory is walked at its
// target, and every path is reported under the link (review 0925a F1:
// WalkDir alone lists nothing and reports no error).
func TestListFilesSymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	for _, p := range []string{
		filepath.Join(target, "-folder", "s.jsonl"),
		filepath.Join(target, "-folder", "s", "subagents", "agent-a.jsonl"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir, "projects")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, skipped, err := ListFiles(root, matchJSONLForTest)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	want := []string{
		filepath.Join(root, "-folder", "s", "subagents", "agent-a.jsonl"),
		filepath.Join(root, "-folder", "s.jsonl"),
	}
	if !slices.Equal(files, want) || len(skipped) != 0 {
		t.Fatalf("files %v skipped %v, want %v under the link", files, skipped, want)
	}

	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if files, _, err := ListFiles(dangling, matchJSONLForTest); err == nil {
		t.Fatalf("dangling root: files %v, want an error", files)
	}
}

// A symlinked root whose target cannot be read is an error, as an
// unreadable plain root is.
func TestListFilesSymlinkedRootUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based test cannot run as root")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.MkdirAll(target, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o755) })
	root := filepath.Join(dir, "projects")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if files, _, err := ListFiles(root, matchJSONLForTest); err == nil {
		t.Fatalf("unreadable target: files %v, want an error", files)
	}
}
