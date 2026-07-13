package cycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/mistakeknot/Remontoire/internal/domain"
	"github.com/mistakeknot/Remontoire/internal/strictjson"
)

// ReplayInput binds a receipt input artifact to the exact bytes verified by replay.
type ReplayInput struct {
	Artifact domain.Artifact
	Data     []byte
}

func ValidateReplayObservation(cycle domain.Cycle, observation Observation, inputs []ReplayInput) error {
	if observation.SchemaVersion != ObservationSchemaV1 || observation.CycleID != cycle.ID || observation.Portfolio != cycle.Portfolio || observation.CapturedAt.IsZero() {
		return fmt.Errorf("replay observation identity, schema, or capture time is invalid")
	}
	required := map[string]bool{
		"beads": false, "discoveries": false, "interest-profile": false,
		"ockham": false, "outcomes": false, "observation": false,
	}
	inputByKind := make(map[string]ReplayInput, len(inputs))
	for _, input := range inputs {
		kind := input.Artifact.Kind
		if _, exists := inputByKind[kind]; exists {
			return fmt.Errorf("replay input artifact %s is ambiguous", kind)
		}
		if _, ok := required[kind]; !ok && kind != "roadmap" {
			return fmt.Errorf("replay input artifact kind %s is invalid", kind)
		}
		inputByKind[kind] = input
		if _, ok := required[kind]; ok {
			required[kind] = true
		}
	}
	for kind, present := range required {
		if !present {
			return fmt.Errorf("replay observation input %s is missing", kind)
		}
	}

	seen := map[string]bool{}
	for _, artifact := range observation.Artifacts {
		if seen[artifact.Kind] {
			return fmt.Errorf("replay observation artifact %s is duplicated", artifact.Kind)
		}
		seen[artifact.Kind] = true
		if artifact.Kind == "observation" {
			return fmt.Errorf("replay observation cannot contain itself")
		}
		input, ok := inputByKind[artifact.Kind]
		if !ok || input.Artifact.Path != artifact.Path || input.Artifact.Digest != artifact.Digest {
			return fmt.Errorf("replay observation artifact %s does not match the receipt input", artifact.Kind)
		}
	}
	for kind := range inputByKind {
		if kind != "observation" && !seen[kind] {
			return fmt.Errorf("replay receipt input %s is not bound by the observation", kind)
		}
	}
	for _, kind := range []string{"beads", "discoveries", "interest-profile", "ockham", "outcomes"} {
		if !seen[kind] {
			return fmt.Errorf("replay observation artifact %s is missing", kind)
		}
	}
	roadmap, hasRoadmap := inputByKind["roadmap"]
	if hasRoadmap && observation.RoadmapDigest != roadmap.Artifact.Digest {
		return fmt.Errorf("replay observation roadmap digest does not match the receipt input")
	}
	if !hasRoadmap && observation.RoadmapDigest != "" {
		return fmt.Errorf("replay observation has a roadmap digest without a receipt input")
	}

	components := map[string]any{
		"beads":            observation.Beads,
		"discoveries":      observation.Discoveries,
		"interest-profile": observation.InterestProfile,
		"ockham": struct {
			Weights  map[string]int `json:"weights"`
			Degraded bool           `json:"degraded"`
		}{Weights: observation.OckhamWeights, Degraded: observation.OckhamDegraded},
		"outcomes": observation.PriorOutcomes,
	}
	for kind, expected := range components {
		matches, err := replayJSONMatches(inputByKind[kind].Data, expected)
		if err != nil {
			return fmt.Errorf("decode replay %s input: %w", kind, err)
		}
		if !matches {
			return fmt.Errorf("replay observation %s does not match its component input", kind)
		}
	}
	return nil
}

func ValidateReplayJudgment(judgment domain.Judgment, observation Observation) error {
	if err := domain.ValidateJudgment(judgment); err != nil {
		return err
	}
	if err := validateEvidenceBindings(judgment, observation); err != nil {
		return err
	}
	return validateSelectedRanking(judgment)
}

func replayJSONMatches(data []byte, expected any) (bool, error) {
	actual, err := decodeReplayValue(data)
	if err != nil {
		return false, err
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	expectedValue, err := decodeReplayValue(expectedJSON)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(actual, expectedValue), nil
}

func decodeReplayValue(data []byte) (any, error) {
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}
