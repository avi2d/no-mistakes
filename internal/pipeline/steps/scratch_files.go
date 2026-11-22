package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

func scratchFileSnapshot(sctx *pipeline.StepContext) (map[string]bool, error) {
	raw, err := stepGitRunRaw(sctx, "ls-files", "-c", "-o", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list worktree files: %w", err)
	}
	files := make(map[string]bool)
	for _, path := range strings.Split(raw, "\x00") {
		if path != "" {
			files[path] = true
		}
	}
	return files, nil
}

func recordAgentScratchFiles(sctx *pipeline.StepContext, before map[string]bool, output []byte) error {
	after, err := scratchFileSnapshot(sctx)
	if err != nil {
		return err
	}
	var result struct {
		EditedPaths *[]string `json:"edited_paths"`
	}
	validOutput := json.Unmarshal(output, &result) == nil
	intended := make(map[string]bool)
	if validOutput && result.EditedPaths != nil {
		for _, path := range *result.EditedPaths {
			intended[path] = true
		}
	}
	for path := range after {
		if before[path] || intended[path] {
			continue
		}
		if err := sctx.DB.AddRunScratchFile(sctx.Run.ID, path); err != nil {
			return err
		}
		sctx.Log(fmt.Sprintf("warning: leaving agent scratch file uncommitted: %s", path))
	}
	return nil
}

func refuseCommittedScratchFiles(sctx *pipeline.StepContext, beforeHead string) error {
	raw, err := stepGitRunRaw(sctx, "diff", "--name-only", "--diff-filter=A", "-z", beforeHead+"..HEAD")
	if err != nil {
		return fmt.Errorf("inspect agent commits for scratch files: %w", err)
	}
	scratch, err := sctx.DB.RunScratchFiles(sctx.Run.ID)
	if err != nil {
		return err
	}
	added := make(map[string]bool)
	for _, path := range strings.Split(raw, "\x00") {
		added[path] = true
	}
	for _, path := range scratch {
		if added[path] {
			return fmt.Errorf("agent committed scratch file %q; refusing to publish the commit", path)
		}
	}
	return nil
}

func schemaWithEditedPaths(schema json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(schema, &object); err != nil {
		return nil, err
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(object["properties"], &properties); err != nil {
		return nil, err
	}
	properties["edited_paths"] = json.RawMessage(`{"type":"array","items":{"type":"string"}}`)
	encoded, err := json.Marshal(properties)
	if err != nil {
		return nil, err
	}
	object["properties"] = encoded
	return json.Marshal(object)
}
