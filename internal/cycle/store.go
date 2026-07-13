package cycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mistakeknot/Remontoire/internal/domain"
)

type FileStore struct {
	Root string
}

func (s FileStore) CycleDir(cycleID string) (string, error) {
	if !safeSegment(cycleID) {
		return "", fmt.Errorf("unsafe cycle id %q", cycleID)
	}
	if s.Root == "" {
		return "", fmt.Errorf("artifact root is required")
	}
	return filepath.Join(s.Root, "cycles", cycleID), nil
}

func (s FileStore) Path(cycleID, name string) (string, error) {
	if !safeSegment(name) {
		return "", fmt.Errorf("unsafe artifact name %q", name)
	}
	dir, err := s.CycleDir(cycleID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (s FileStore) WriteJSON(cycleID, kind, name string, value any) (domain.Artifact, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("marshal %s: %w", kind, err)
	}
	data = append(data, '\n')
	return s.WriteBytes(cycleID, kind, name, data)
}

func (s FileStore) WriteBytes(cycleID, kind, name string, data []byte) (domain.Artifact, error) {
	path, err := s.Path(cycleID, name)
	if err != nil {
		return domain.Artifact{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return domain.Artifact{}, fmt.Errorf("create cycle artifact directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+name+".tmp-")
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("create temporary artifact: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return domain.Artifact{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return domain.Artifact{}, fmt.Errorf("write temporary artifact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return domain.Artifact{}, fmt.Errorf("sync temporary artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return domain.Artifact{}, fmt.Errorf("close temporary artifact: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return domain.Artifact{}, fmt.Errorf("replace artifact: %w", err)
	}
	sum := sha256.Sum256(data)
	return domain.Artifact{Kind: kind, Path: path, Digest: hex.EncodeToString(sum[:])}, nil
}

func (s FileStore) WriteCycle(cycle domain.Cycle) (domain.Artifact, error) {
	return s.WriteJSON(cycle.ID, "cycle-state", "cycle.json", cycle)
}

func (s FileStore) HashExisting(kind, path string) (domain.Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Artifact{}, err
	}
	sum := sha256.Sum256(data)
	return domain.Artifact{Kind: kind, Path: path, Digest: hex.EncodeToString(sum[:])}, nil
}

func safeSegment(value string) bool {
	return value != "" && value == filepath.Base(value) && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}
