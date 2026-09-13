"use client";

import { useMemo } from "react";
import type { WorkflowDefinition, WorkflowStep } from "@multica/core/workflows";
import { WorkflowCanvas } from "../../canvas/workflow-canvas";
import { definitionToGraph } from "../../graph";

const ignore = () => {};

/** The pinned definition supplies topology; attempts supply only observed node states. */
export function WorkflowRunGraph({
  definition,
  steps,
  selected,
  onSelect,
}: {
  definition: WorkflowDefinition;
  steps: WorkflowStep[];
  selected: string | null;
  onSelect: (key: string | null) => void;
}) {
  const graph = useMemo(() => {
    const result = definitionToGraph(definition);
    const latest = new Map<string, WorkflowStep>();
    for (const step of steps) {
      const old = latest.get(step.node_key);
      if (!old || step.attempt > old.attempt) latest.set(step.node_key, step);
    }
    return {
      ...result,
      nodes: result.nodes.map((node) => ({
        ...node,
        data: {
          ...node.data,
          executionStatus: latest.get(node.data.nodeKey)?.status,
        },
      })),
    };
  }, [definition, steps]);
  return (
    <div
      className="flex h-96 min-h-0 overflow-hidden rounded-lg border"
      data-testid="workflow-run-graph"
    >
      <WorkflowCanvas
        nodes={graph.nodes}
        edges={graph.edges}
        readOnly
        selectedNodeId={selected}
        onSelectNode={onSelect}
        onNodesChange={ignore}
        onEdgesChange={ignore}
        onConnect={ignore}
      />
    </div>
  );
}
