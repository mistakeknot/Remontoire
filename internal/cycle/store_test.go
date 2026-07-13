package cycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

func TestFileStoreWritesAtomicHashedArtifacts(t *testing.T) {
	root := t.TempDir()
	store := FileStore{Root: root}
	artifact, err := store.WriteJSON("cycle-1", "input", "beads.json", map[string]any{"beads": []string{"Revel-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Digest) != 64 || artifact.Kind != "input" {
		t.Fatalf("artifact = %#v", artifact)
	}
	if !strings.HasSuffix(artifact.Path, filepath.Join("cycle-1", "beads.json")) {
		t.Fatalf("artifact path = %q", artifact.Path)
	}
	data, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("artifact is not JSON: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "cycles", "cycle-1", ".*.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %#v", matches)
	}
}

func TestFileStoreRejectsUnsafeArtifactName(t *testing.T) {
	store := FileStore{Root: t.TempDir()}
	if _, err := store.WriteJSON("cycle-1", "input", "../escape.json", map[string]any{}); err == nil {
		t.Fatal("unsafe artifact name was accepted")
	}
}

func TestFileStoreWritesCycleProjection(t *testing.T) {
	store := FileStore{Root: t.TempDir()}
	cycle := domain.Cycle{SchemaVersion: domain.CycleSchemaV1, ID: "cycle-1", Portfolio: "sylveste", Stage: domain.StageObserving}
	artifact, err := store.WriteCycle(cycle)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "cycle-state" || !strings.HasSuffix(artifact.Path, "cycle.json") {
		t.Fatalf("artifact = %#v", artifact)
	}
}
