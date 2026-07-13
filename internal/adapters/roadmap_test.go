package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRoadmapSyncReturnsOutputDigest(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "docs", "roadmap.json")
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"project":"fixture"}`)
	if err := os.WriteFile(output, content, 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	roadmap := Roadmap{BashBinary: "/bin/bash", ScriptPath: "/plugins/interpath/sync-roadmap-json.sh", Dir: dir, OutputPath: output, Runner: runner}

	digest, err := roadmap.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %q", digest)
	}
	want := []string{roadmap.ScriptPath, output}
	if got := runner.calls[0].Invocation.Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
