package schemas_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mistakeknot/Remontoire/internal/strictjson"
)

func TestPublishedSchemasAreValidRootContracts(t *testing.T) {
	for _, name := range []string{"judgment-v1.json", "execution-v1.json", "review-v1.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := strictjson.RejectDuplicateKeys(data); err != nil {
				t.Fatalf("duplicate JSON key: %v", err)
			}
			var schema map[string]any
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatalf("unexpected $schema: %v", schema["$schema"])
			}
			id, ok := schema["$id"].(string)
			if !ok || !strings.HasSuffix(id, "/schemas/"+filepath.Base(name)) {
				t.Fatalf("unexpected $id: %v", schema["$id"])
			}
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("root must be a closed object schema")
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok || len(properties) == 0 {
				t.Fatalf("root properties must be a non-empty object")
			}
			required, ok := schema["required"].([]any)
			if !ok || len(required) == 0 {
				t.Fatalf("root required must be a non-empty array")
			}
			for _, raw := range required {
				field, ok := raw.(string)
				if !ok || properties[field] == nil {
					t.Fatalf("required property %v has no schema", raw)
				}
			}
			validateLocalRefs(t, schema, schema)
		})
	}
}

func TestPublishedSchemasUseTypedConstAndEnumNodes(t *testing.T) {
	for _, name := range []string{"judgment-v1.json", "execution-v1.json", "review-v1.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			validateTypedConstAndEnumNodes(t, "$", schema)
		})
	}
}

func TestJudgmentSchemaConstrainsCanonicalEvidenceKinds(t *testing.T) {
	data, err := os.ReadFile("judgment-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	got := schema.Definitions["evidence"].Properties["kind"].Enum
	want := []string{"bead", "discovery", "policy", "roadmap", "outcome"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence kinds = %v, want %v", got, want)
	}
}

func validateTypedConstAndEnumNodes(t *testing.T, path string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if (typed["const"] != nil || typed["enum"] != nil) && typed["type"] == nil {
			t.Errorf("%s: const and enum schema nodes must declare type", path)
		}
		for key, child := range typed {
			validateTypedConstAndEnumNodes(t, path+"/"+key, child)
		}
	case []any:
		for index, child := range typed {
			validateTypedConstAndEnumNodes(t, fmt.Sprintf("%s/%d", path, index), child)
		}
	}
}

func validateLocalRefs(t *testing.T, root map[string]any, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			current := any(root)
			for _, encoded := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				segment := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
				object, ok := current.(map[string]any)
				if !ok {
					t.Fatalf("local ref %q traverses a non-object at %q", ref, segment)
				}
				current, ok = object[segment]
				if !ok {
					t.Fatalf("local ref %q does not resolve", ref)
				}
			}
		}
		for _, child := range typed {
			validateLocalRefs(t, root, child)
		}
	case []any:
		for _, child := range typed {
			validateLocalRefs(t, root, child)
		}
	}
}
