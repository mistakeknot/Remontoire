package strictjson

import (
	"strings"
	"testing"
)

func TestRejectDuplicateKeysRecursively(t *testing.T) {
	for name, input := range map[string]string{
		"root":   `{"mode":"shadow","mode":"proposal"}`,
		"nested": `{"contract":{"repository":"/one","repository":"/two"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := RejectDuplicateKeys([]byte(input)); err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRejectDuplicateKeysAllowsSameKeyInSeparateObjects(t *testing.T) {
	if err := RejectDuplicateKeys([]byte(`[{"id":"one"},{"id":"two"}]`)); err != nil {
		t.Fatal(err)
	}
}

func TestRejectDuplicateKeysRejectsTrailingJSON(t *testing.T) {
	if err := RejectDuplicateKeys([]byte(`{"id":"one"} {"id":"two"}`)); err == nil {
		t.Fatal("expected trailing JSON error")
	}
}
