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

func TestRejectNonExactFieldsRecursively(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	type payload struct {
		Items []item `json:"items"`
	}
	var target payload
	if err := RejectNonExactFields([]byte(`{"items":[{"name":"exact"}]}`), &target); err != nil {
		t.Fatal(err)
	}
	if err := RejectNonExactFields([]byte(`{"items":[{"Name":"inexact"}]}`), &target); err == nil || !strings.Contains(err.Error(), "exact field") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectNonExactFieldsRejectsNullCollection(t *testing.T) {
	type payload struct {
		Items []string `json:"items"`
	}
	var target payload
	if err := RejectNonExactFields([]byte(`{"items":null}`), &target); err == nil || !strings.Contains(err.Error(), "array") {
		t.Fatalf("error = %v", err)
	}
}
