"use client";

import { useQuery } from "@tanstack/react-query";
import type { WorkflowDefinition, WorkflowNode } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions } from "@multica/core/workspace/queries";
import { Input } from "@multica/ui/components/ui/input";
import { emptyRouting } from "../graph";
import { useT } from "../../i18n";
import {
  PanelHint,
  PanelSection,
  PanelSelect,
  PanelSelectField,
  PanelField,
  type PanelOption,
} from "./panel-controls";

/**
 * The `AGENT 分派策略` section: how an agent node picks the Agent that runs it.
 *
 * This is the only part of the properties panel that reads server state, and it
 * reads it through the existing `agentListOptions` rather than a query of its
 * own - the Agents page, the assignee pickers and this select must agree about
 * what exists in the workspace, and a second cache key would let them disagree.
 *
 * The three strategies are not interchangeable presentations of one field. Each
 * makes exactly one *other* field mandatory (`validateRouting` in
 * server/internal/workflow/validate.go), so the section renders the strategy's
 * field and nothing else: showing all three at once would invite an author to
 * fill in a `capability` that an explicit route will never read, then wonder why
 * changing it does nothing.
 */

/** Mirrors `RoutingStrategy` in server/internal/workflow/definition.go. */
const ROUTING_STRATEGIES = ["explicit", "previous_step", "capability"] as const;

export function AgentRoutingSection({
  node,
  definition,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  definition: WorkflowDefinition;
  readOnly: boolean;
  onChange(next: WorkflowNode): void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  // `enabled` on the workspace id, not on the strategy: the fallback Agent field
  // is available for every strategy, so the list is needed regardless. Archived
  // agents are included by `agentListOptions` on purpose - see `agentOptions`.
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: wsId !== "",
  });

  // An agent node whose routing the server omitted (or sent as null) still has
  // to be editable; `emptyRouting` is the complete zero value the wire type
  // needs, since none of its members are optional.
  const routing = node.routing ?? emptyRouting();

  const patchRouting = (patch: Partial<typeof routing>) => {
    onChange({ ...node, routing: { ...routing, ...patch } });
  };

  const strategyLabels: Record<(typeof ROUTING_STRATEGIES)[number], string> = {
    explicit: t(($) => $.panel.routing.strategy_explicit),
    previous_step: t(($) => $.panel.routing.strategy_previous_step),
    capability: t(($) => $.panel.routing.strategy_capability),
  };

  const strategyOptions: PanelOption[] = [
    { value: "", label: t(($) => $.panel.routing.strategy_unset) },
    ...ROUTING_STRATEGIES.map((strategy) => ({
      value: strategy,
      label: strategyLabels[strategy],
    })),
  ];
  // A strategy this build has never heard of is offered back as its raw token.
  // The alternative - dropping it - would make the select render as "not set"
  // and turn merely *looking* at the node into an edit that clears a routing the
  // server understands perfectly well.
  if (
    routing.strategy !== "" &&
    !(ROUTING_STRATEGIES as readonly string[]).includes(routing.strategy)
  ) {
    strategyOptions.push({
      value: routing.strategy,
      label: t(($) => $.panel.routing.strategy_unknown, {
        strategy: routing.strategy,
      }),
    });
  }

  return (
    <PanelSection title={t(($) => $.panel.section.routing)}>
      <PanelSelectField
        label={t(($) => $.panel.routing.strategy)}
        problem={
          routing.strategy === ""
            ? t(($) => $.panel.routing.strategy_required)
            : undefined
        }
      >
        <PanelSelect
          value={routing.strategy}
          options={strategyOptions}
          // The other strategies' fields are deliberately NOT cleared when the
          // strategy changes. The validator only reads the field the active
          // strategy names, so a stale `capability` is harmless - and an author
          // comparing two routings would otherwise lose the first one's Agent
          // the moment they looked at the second.
          onChange={(next) => patchRouting({ strategy: next })}
          disabled={readOnly}
          ariaLabel={t(($) => $.panel.routing.strategy_aria)}
        />
      </PanelSelectField>

      {routing.strategy === "explicit" ? (
        <AgentPicker
          label={t(($) => $.panel.routing.agent)}
          ariaLabel={t(($) => $.panel.routing.agent_aria)}
          value={routing.agent_id}
          agents={agents}
          readOnly={readOnly}
          onChange={(next) => patchRouting({ agent_id: next })}
          problem={
            routing.agent_id === ""
              ? t(($) => $.panel.routing.agent_required)
              : undefined
          }
        />
      ) : null}

      {routing.strategy === "previous_step" ? (
        <PreviousStepPicker
          node={node}
          definition={definition}
          value={routing.from_node}
          readOnly={readOnly}
          onChange={(next) => patchRouting({ from_node: next })}
        />
      ) : null}

      {routing.strategy === "capability" ? (
        <PanelField
          label={t(($) => $.panel.routing.capability)}
          problem={
            routing.capability === ""
              ? t(($) => $.panel.routing.capability_required)
              : undefined
          }
        >
          <Input
            value={routing.capability}
            disabled={readOnly}
            placeholder={t(($) => $.panel.routing.capability_placeholder)}
            onChange={(event) =>
              patchRouting({ capability: event.target.value })
            }
          />
        </PanelField>
      ) : null}

      {/* Available under every strategy, because it is the last resort in the
          routing order rather than an alternative to one of them. */}
      <AgentPicker
        label={t(($) => $.panel.routing.fallback)}
        ariaLabel={t(($) => $.panel.routing.fallback_aria)}
        hint={t(($) => $.panel.routing.fallback_hint)}
        value={routing.fallback_agent_id}
        agents={agents}
        readOnly={readOnly}
        onChange={(next) => patchRouting({ fallback_agent_id: next })}
      />
    </PanelSection>
  );
}

/** The subset of `Agent` this picker needs; anything more would couple it to
 *  fields the routing contract does not mention. */
type AgentChoice = {
  id: string;
  name: string;
  archived_at: string | null;
};

function AgentPicker({
  label,
  ariaLabel,
  hint,
  value,
  agents,
  readOnly,
  onChange,
  problem,
}: {
  label: string;
  ariaLabel: string;
  hint?: string;
  value: string;
  agents: readonly AgentChoice[];
  readOnly: boolean;
  onChange(next: string): void;
  problem?: string;
}) {
  const { t } = useT("workflows");

  const options: PanelOption[] = [
    { value: "", label: t(($) => $.panel.routing.agent_unset) },
  ];
  for (const agent of agents) {
    // An archived agent is still a legal routing target for an already-published
    // template, so it is labelled rather than hidden - the author needs to see
    // *why* the step stopped picking anyone up.
    options.push({
      value: agent.id,
      label: agent.archived_at
        ? t(($) => $.panel.routing.agent_archived, { name: agent.name })
        : agent.name,
    });
  }
  // A pinned agent that is not in the list (deleted, or in another workspace
  // because the graph was imported) keeps its id visible. Base UI's Select
  // renders nothing for a value with no matching item, so without this the field
  // would look empty while still saving the id - the worst of both.
  if (value !== "" && !agents.some((agent) => agent.id === value)) {
    options.push({
      value,
      label: t(($) => $.panel.routing.agent_missing, { id: value }),
    });
  }

  return (
    <PanelSelectField label={label} hint={hint} problem={problem}>
      <PanelSelect
        value={value}
        options={options}
        onChange={onChange}
        disabled={readOnly}
        ariaLabel={ariaLabel}
      />
      {agents.length === 0 ? (
        <PanelHint>{t(($) => $.panel.routing.agent_empty)}</PanelHint>
      ) : null}
    </PanelSelectField>
  );
}

/**
 * `from_node` picker.
 *
 * Only agent nodes are offered, and never the node itself: the validator rejects
 * both ("routes from %q, which is a %s node and has no Agent to reuse", "routes
 * from itself"). Filtering here rather than reporting it afterwards is the one
 * place the panel can make an invalid graph unrepresentable instead of merely
 * detectable.
 */
function PreviousStepPicker({
  node,
  definition,
  value,
  readOnly,
  onChange,
}: {
  node: WorkflowNode;
  definition: WorkflowDefinition;
  value: string;
  readOnly: boolean;
  onChange(next: string): void;
}) {
  const { t } = useT("workflows");

  const candidates = definition.nodes.filter(
    (candidate) => candidate.type === "agent" && candidate.key !== node.key,
  );

  const options: PanelOption[] = [
    { value: "", label: t(($) => $.panel.routing.from_node_unset) },
    ...candidates.map((candidate) => ({
      value: candidate.key,
      label: candidate.name || candidate.key,
    })),
  ];
  // Same reasoning as the agent picker: a dangling `from_node` must stay visible
  // so the author can see the key the validator is complaining about.
  if (value !== "" && !candidates.some((candidate) => candidate.key === value)) {
    options.push({
      value,
      label: t(($) => $.panel.routing.from_node_missing, { key: value }),
    });
  }

  return (
    <PanelSelectField
      label={t(($) => $.panel.routing.from_node)}
      problem={
        value === "" ? t(($) => $.panel.routing.from_node_required) : undefined
      }
    >
      <PanelSelect
        value={value}
        options={options}
        onChange={onChange}
        disabled={readOnly}
        ariaLabel={t(($) => $.panel.routing.from_node_aria)}
      />
      {candidates.length === 0 ? (
        <PanelHint>{t(($) => $.panel.routing.from_node_empty)}</PanelHint>
      ) : null}
    </PanelSelectField>
  );
}
