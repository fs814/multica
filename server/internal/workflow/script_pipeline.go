package workflow

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/pkg/scriptpipeline"
)

// Only the direct agent successor of a scripts intake executes the pipeline.
// Later agent/review nodes keep their normal model-backed behavior.
func ScriptPipelineForNode(def *Definition, node *Node, input []byte) (*scriptpipeline.Config, error) {
	if def == nil || node == nil || node.Type != NodeTypeAgent {
		return nil, nil
	}
	entry, ok := def.NodeByKey(def.EntryNode)
	if !ok || entry.EffectiveInputMode() != InputModeScripts || len(entry.Next) != 1 || entry.Next[0] != node.Key {
		return nil, nil
	}
	config, err := scriptpipeline.Resolve(entry.ScriptPipeline, input)
	if err != nil {
		return nil, newEngineError(ErrCodeInvalidSubmission, err.Error())
	}
	return config, nil
}

// Change only the execution snapshot; the saved instance and historical run stay intact.
func singleScriptStepInput(definition, input []byte, step string) ([]byte, error) {
	if step != "clone" && step != "build" && step != "run" {
		return nil, newEngineError(ErrCodeInvalidSubmission, "script_step must be clone, build, or run")
	}
	def, err := ParseDefinition(definition)
	if err != nil {
		return nil, err
	}
	entry, ok := def.NodeByKey(def.EntryNode)
	if !ok || entry.EffectiveInputMode() != InputModeScripts {
		return nil, newEngineError(ErrCodeInvalidSubmission, "single-step execution requires directory script input")
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(input, &values) != nil || values == nil {
		return nil, newEngineError(ErrCodeInvalidSubmission, "input must be a string-valued object")
	}
	steps, _ := json.Marshal([]string{step})
	values["script_steps"], _ = json.Marshal(string(steps))
	return json.Marshal(values)
}
