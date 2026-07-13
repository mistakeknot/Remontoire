package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

var inputKinds = map[string]bool{
	"beads": true, "discoveries": true, "interest-profile": true,
	"ockham": true, "roadmap": true, "outcomes": true, "observation": true,
}

func Build(cycle domain.Cycle, terminalAt time.Time) (domain.Receipt, error) {
	if cycle.ID == "" || cycle.SchemaVersion != domain.CycleSchemaV1 {
		return domain.Receipt{}, fmt.Errorf("valid cycle projection is required")
	}
	if terminalAt.IsZero() {
		return domain.Receipt{}, fmt.Errorf("terminal time is required")
	}

	terminal, err := cloneCycle(cycle)
	if err != nil {
		return domain.Receipt{}, err
	}
	if terminal.Stage != domain.StageFailed {
		terminal.Stage = domain.StageCompleted
	}
	terminal.UpdatedAt = terminalAt.UTC()
	terminal.SignedReceiptID = ""
	for key := range terminal.IdempotencyKeys {
		if strings.HasPrefix(key, "receipt:") || key == "event:completed" {
			delete(terminal.IdempotencyKeys, key)
		}
	}
	terminal.Artifacts = filterAndSortArtifacts(terminal.Artifacts)

	inputs := make([]domain.Artifact, 0, len(terminal.Artifacts))
	outputs := make([]domain.Artifact, 0, len(terminal.Artifacts))
	for _, artifact := range terminal.Artifacts {
		if inputKinds[artifact.Kind] {
			inputs = append(inputs, artifact)
		} else {
			outputs = append(outputs, artifact)
		}
	}
	decision, err := json.Marshal(terminal)
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("marshal terminal decision: %w", err)
	}
	sum := sha256.Sum256(decision)
	return domain.Receipt{
		SchemaVersion: domain.ReceiptSchemaV1,
		Cycle:         terminal, InputArtifacts: inputs, OutputArtifacts: outputs,
		DecisionHash: hex.EncodeToString(sum[:]), TerminalAt: terminalAt.UTC(),
	}, nil
}

func Marshal(value domain.Receipt) ([]byte, string, error) {
	if value.SchemaVersion != domain.ReceiptSchemaV1 || value.DecisionHash == "" {
		return nil, "", fmt.Errorf("valid receipt is required")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("marshal receipt: %w", err)
	}
	data = append(data, '\n')
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func cloneCycle(cycle domain.Cycle) (domain.Cycle, error) {
	data, err := json.Marshal(cycle)
	if err != nil {
		return domain.Cycle{}, fmt.Errorf("marshal cycle projection: %w", err)
	}
	var clone domain.Cycle
	if err := json.Unmarshal(data, &clone); err != nil {
		return domain.Cycle{}, fmt.Errorf("clone cycle projection: %w", err)
	}
	return clone, nil
}

func filterAndSortArtifacts(artifacts []domain.Artifact) []domain.Artifact {
	result := make([]domain.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Kind != "receipt" && artifact.Kind != "receipt-signature" {
			result = append(result, artifact)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].Digest < result[j].Digest
	})
	return result
}
