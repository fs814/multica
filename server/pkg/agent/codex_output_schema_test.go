package agent

import (
	"encoding/json"
	"testing"
)

func TestCodexOutputSchemaIsAnObjectOnTheWire(t *testing.T) {
	params := map[string]any{"threadId": "thread-a"}
	applyCodexOutputSchema(params, nil)
	if _, ok := params["outputSchema"]; ok {
		t.Fatal("unconstrained turns must omit the field")
	}
	applyCodexOutputSchema(params, json.RawMessage(`{"type":"object","required":["verdict"]}`))
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	schema, ok := wire["outputSchema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("output schema is not an object: %s", raw)
	}
}
