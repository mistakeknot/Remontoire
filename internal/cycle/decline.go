package cycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/adapters"
	"github.com/mistakeknot/Remontoire/internal/domain"
)

func (s *Service) Decline(ctx context.Context, cycleID, actor, reason string) (cycle domain.Cycle, err error) {
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if actor == "" || strings.EqualFold(actor, "remontoire") {
		return domain.Cycle{}, fmt.Errorf("decline actor must identify an external principal")
	}
	if reason == "" {
		return domain.Cycle{}, fmt.Errorf("decline reason is required")
	}
	if err := s.validate(); err != nil {
		return domain.Cycle{}, err
	}
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	owner := "remontoire:" + cycleID
	if err := s.Kernel.AcquireCycleLock(ctx, cycle.Portfolio, owner, s.Config.LockTimeout); err != nil {
		return domain.Cycle{}, err
	}
	defer func() {
		releaseErr := s.Kernel.ReleaseCycleLock(context.WithoutCancel(ctx), cycle.Portfolio, owner)
		if err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	cycle, err = s.Kernel.GetCycle(ctx, cycleID)
	if err != nil {
		return domain.Cycle{}, err
	}
	if err := s.ensureStageEvent(ctx, &cycle); err != nil {
		return cycle, err
	}
	if cycle.Decline != nil && (cycle.Decline.Actor != actor || cycle.Decline.Reason != reason) {
		return cycle, fmt.Errorf("decline record conflicts with the existing principal decision")
	}
	if cycle.Stage == domain.StageCompleted && cycle.Decline != nil {
		return cycle, nil
	}
	if cycle.Stage != domain.StageAwaitingApproval && cycle.Stage != domain.StageDeclined {
		return cycle, fmt.Errorf("cycle %s cannot be declined from stage %s", cycle.ID, cycle.Stage)
	}
	if cycle.Candidate == nil || cycle.ExperimentBeadID == "" || cycle.ContractHash == "" {
		return cycle, fmt.Errorf("cycle %s has no proposed experiment to decline", cycle.ID)
	}
	if cycle.Decline == nil {
		decision := domain.Decline{
			SchemaVersion: domain.DeclineSchemaV1, CycleID: cycle.ID, ContractHash: cycle.ContractHash,
			Actor: actor, Reason: reason, DeclinedAt: s.now(),
		}
		artifact, writeErr := s.Store.WriteJSON(cycle.ID, "decline", "decline.json", decision)
		if writeErr != nil {
			return cycle, writeErr
		}
		cycle.Decline = &decision
		appendArtifact(&cycle, artifact)
		if err := s.persist(ctx, &cycle); err != nil {
			return cycle, err
		}
	}
	if cycle.Stage == domain.StageAwaitingApproval {
		if err := s.transition(ctx, &cycle, domain.StageDeclined); err != nil {
			return cycle, err
		}
	}
	if cycle.IdempotencyKeys["decline:close"] != "completed" {
		items, listErr := s.Backlog.List(ctx)
		if listErr != nil {
			return cycle, fmt.Errorf("decline read backlog: %w", listErr)
		}
		if experiment, ok := adapters.FindBead(items, cycle.ExperimentBeadID); !ok || !strings.EqualFold(experiment.Status, "closed") {
			if closeErr := s.Backlog.Close(ctx, cycle.ExperimentBeadID, "Declined by "+actor+": "+reason); closeErr != nil {
				return cycle, fmt.Errorf("decline close experiment: %w", closeErr)
			}
		}
		cycle.IdempotencyKeys["decline:close"] = "completed"
		if err := s.persist(ctx, &cycle); err != nil {
			return cycle, err
		}
	}
	if err := s.transition(ctx, &cycle, domain.StageCompleted); err != nil {
		return cycle, err
	}
	return cycle, nil
}
