package cycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

func (s *Service) observe(ctx context.Context, cycle domain.Cycle) (Observation, error) {
	beads, err := s.Backlog.List(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("read canonical backlog: %w", err)
	}
	discoveries, profile, err := s.Kernel.Observation(ctx, s.Config.DiscoveryLimit)
	if err != nil {
		return Observation{}, fmt.Errorf("read intercore discovery state: %w", err)
	}
	weights, degraded := s.Policy.Weights(ctx)
	outcomes, err := s.Kernel.ListOutcomes(ctx, s.Config.DiscoveryLimit)
	if err != nil {
		return Observation{}, fmt.Errorf("read prior outcome state: %w", err)
	}

	artifacts := make([]domain.Artifact, 0, 6)
	for _, input := range []struct {
		kind string
		name string
		data any
	}{
		{kind: "beads", name: "beads.json", data: beads},
		{kind: "discoveries", name: "discoveries.json", data: discoveries},
		{kind: "interest-profile", name: "interest-profile.json", data: profile},
		{kind: "ockham", name: "ockham.json", data: map[string]any{"weights": weights, "degraded": degraded}},
		{kind: "outcomes", name: "outcomes.json", data: outcomes},
	} {
		artifact, err := s.Store.WriteJSON(cycle.ID, input.kind, input.name, input.data)
		if err != nil {
			return Observation{}, err
		}
		artifacts = append(artifacts, artifact)
	}
	roadmapDigest := ""
	if s.Config.RoadmapPath != "" {
		data, err := os.ReadFile(s.Config.RoadmapPath)
		if err == nil {
			artifact, err := s.Store.WriteBytes(cycle.ID, "roadmap", "roadmap.json", data)
			if err != nil {
				return Observation{}, fmt.Errorf("snapshot roadmap: %w", err)
			}
			roadmapDigest = artifact.Digest
			artifacts = append(artifacts, artifact)
		} else if !os.IsNotExist(err) {
			return Observation{}, fmt.Errorf("read roadmap: %w", err)
		}
	}
	for _, artifact := range artifacts {
		payload, _ := json.Marshal(map[string]string{"sha256": artifact.Digest})
		if err := s.Kernel.RecordReplayInput(ctx, cycle.RunID, artifact.Kind, filepathBase(artifact.Path), string(payload), artifact.Path); err != nil {
			return Observation{}, fmt.Errorf("register replay input %s: %w", artifact.Kind, err)
		}
	}
	return Observation{
		SchemaVersion: ObservationSchemaV1, CycleID: cycle.ID, Portfolio: cycle.Portfolio,
		CapturedAt: s.now(), Beads: beads, Discoveries: discoveries, InterestProfile: profile,
		OckhamWeights: weights, OckhamDegraded: degraded, RoadmapDigest: roadmapDigest,
		PriorOutcomes: outcomes, Artifacts: artifacts,
	}, nil
}

func filepathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
