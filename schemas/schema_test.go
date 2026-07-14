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
	for _, name := range []string{"agency-v1.json", "judgment-v1.json", "execution-v1.json", "review-v1.json"} {
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
	for _, name := range []string{"agency-v1.json", "judgment-v1.json", "execution-v1.json", "review-v1.json"} {
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

func TestAgencyManifestDeclaresFirstClassBoundaries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "agency.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		t.Fatalf("duplicate JSON key: %v", err)
	}
	var manifest struct {
		SchemaVersion string `json:"schema_version"`
		Kind          string `json:"kind"`
		Name          string `json:"name"`
		Layer         string `json:"layer"`
		Class         string `json:"class"`
		Version       string `json:"version"`
		Repository    string `json:"repository"`
		Install       struct {
			Script      string   `json:"script"`
			CheckArgs   []string `json:"check_args"`
			DefaultArgs []string `json:"default_args"`
			SupportedOS []string `json:"supported_os"`
		} `json:"install"`
		Runtime struct {
			Binary         string   `json:"binary"`
			DoctorArgs     []string `json:"doctor_args"`
			StatusArgs     []string `json:"status_args"`
			ServiceManager string   `json:"service_manager"`
			Service        string   `json:"service"`
			Timer          string   `json:"timer"`
		} `json:"runtime"`
		Capabilities []string `json:"capabilities"`
		Authority    struct {
			RequiresApproval []string `json:"requires_approval"`
			Never            []string `json:"never"`
		} `json:"authority"`
		Contracts []string `json:"contracts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.SchemaVersion != "interverse.agency/v1" || manifest.Kind != "agency" || manifest.Name != "remontoire" {
		t.Fatalf("unexpected manifest identity: %#v", manifest)
	}
	if manifest.Layer != "L2" || manifest.Class != "portfolio" || manifest.Version != "0.1.0" {
		t.Fatalf("unexpected agency classification: layer=%q class=%q version=%q", manifest.Layer, manifest.Class, manifest.Version)
	}
	if manifest.Repository != "https://github.com/mistakeknot/Remontoire" {
		t.Fatalf("unexpected repository: %q", manifest.Repository)
	}
	if manifest.Install.Script != "scripts/install.sh" || !reflect.DeepEqual(manifest.Install.CheckArgs, []string{"--check"}) ||
		!reflect.DeepEqual(manifest.Install.DefaultArgs, []string{"--no-enable"}) || !reflect.DeepEqual(manifest.Install.SupportedOS, []string{"linux"}) {
		t.Fatalf("unexpected install contract: %#v", manifest.Install)
	}
	if manifest.Runtime.Binary != "remontoire" || manifest.Runtime.ServiceManager != "systemd-user" ||
		manifest.Runtime.Service != "remontoire.service" || manifest.Runtime.Timer != "remontoire.timer" ||
		!reflect.DeepEqual(manifest.Runtime.DoctorArgs, []string{"doctor", "--json"}) ||
		!reflect.DeepEqual(manifest.Runtime.StatusArgs, []string{"status", "--json"}) {
		t.Fatalf("unexpected runtime contract: %#v", manifest.Runtime)
	}
	if len(manifest.Capabilities) == 0 || len(manifest.Contracts) == 0 {
		t.Fatal("capabilities and contracts must be declared")
	}
	if !reflect.DeepEqual(manifest.Authority.RequiresApproval, []string{"experiment.execute"}) {
		t.Fatalf("unexpected approval boundary: %v", manifest.Authority.RequiresApproval)
	}
	wantNever := []string{"git.push", "git.merge", "deployment.deploy", "release.publish"}
	if !reflect.DeepEqual(manifest.Authority.Never, wantNever) {
		t.Fatalf("unexpected prohibited authority: %v", manifest.Authority.Never)
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
