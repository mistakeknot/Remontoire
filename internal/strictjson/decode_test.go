package strictjson

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

func TestRejectCaseAliasesRejectsCaseVariantsOfKnownFields(t *testing.T) {
	type payload struct {
		Cost *float64 `json:"total_cost_usd"`
		Type string   `json:"type"`
	}
	var target payload
	for name, input := range map[string]string{
		"cost": `{"total_cost_usd":null,"TOTAL_COST_USD":0}`,
		"type": `{"type":"turn.failed","TYPE":"turn.completed"}`,
		"only": `{"Type":"turn.completed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := RejectCaseAliases([]byte(input), &target); err == nil || !strings.Contains(err.Error(), "case variant") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := RejectCaseAliases([]byte(`{"type":"x","total_cost_usd":0,"session_id":"unrelated","Other":1}`), &target); err != nil {
		t.Fatalf("exact and unrelated keys: %v", err)
	}
}

func TestRejectCaseAliasesWalksEmbeddedStructsAndSkipsCustomDecoders(t *testing.T) {
	type usage struct {
		Input *int64 `json:"input_tokens,omitempty"`
	}
	type record struct {
		usage
		At   time.Time       `json:"at"`
		Raw  json.RawMessage `json:"raw"`
		Name string          `json:"name"`
	}
	var target record
	if err := RejectCaseAliases([]byte(`{"input_tokens":1,"at":"2026-10-24T00:00:00Z","raw":{"X":1},"name":"n"}`), &target); err != nil {
		t.Fatal(err)
	}
	if err := RejectCaseAliases([]byte(`{"input_tokens":null,"INPUT_TOKENS":0}`), &target); err == nil {
		t.Fatal("expected a promoted embedded field's case variant to be rejected")
	}
}
