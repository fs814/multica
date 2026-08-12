"use client";

/**
 * The one line an agent node shows below its title.
 *
 * A workflow author's most common question about an agent step is "who runs
 * this?", and the answer lives in a *different field depending on the strategy*:
 * `agent_id` for explicit, `from_node` for previous_step, `capability` for
 * capability. Dumping the routing object would show three empty fields and one
 * full one; reading out only the field the chosen strategy makes meaningful is
 * what makes the card scannable.
 *
 * Strategy is a lenient `z.string()` on the wire (see the header of
 * packages/core/workflows/schemas.ts), so an unrecognised strategy renders as
 * itself rather than being coerced into one of the three - a card must never
 * claim a routing behaviour the engine will not perform.
 *
 * This mirrors `RoutingLine` in components/workflow-template-detail-page.tsx but
 * does not share code with it: that one is a prose row in a read-only list and
 * spells out fallbacks and full sentences, this one has ~26 characters and needs
 * an icon. Merging them would mean one of the two surfaces getting text sized
 * for the other.
 */

import { Ban, Bot, CornerUpLeft, Sparkles, Users } from "lucide-react";
import type { WorkflowNode } from "@multica/core/workflows";
import { useT } from "../../../i18n";
import { WorkflowNodeSummary } from "./workflow-node-card";

export function RoutingSummary({ node }: { node: WorkflowNode }) {
  const { t } = useT("workflows");
  const routing = node.routing;

  // An agent node with no routing is rejected by the validator, so this is a
  // problem the author has to see rather than an empty line to skip over.
  if (!routing || !routing.strategy) {
    return (
      <WorkflowNodeSummary icon={Ban}>
        {t(($) => $.canvas.routing.none)}
      </WorkflowNodeSummary>
    );
  }

  switch (routing.strategy) {
    case "explicit":
      // Labelled by strategy, not by agent name: the canvas has only the agent's
      // UUID (resolving names is the properties panel's job, which has the agent
      // list), and a raw UUID on a card is noise. "Fixed Agent" is still the
      // answer to "who runs this?" at the level a card can answer it.
      return (
        <WorkflowNodeSummary icon={Bot}>
          {t(($) => $.canvas.routing.explicit)}
        </WorkflowNodeSummary>
      );
    case "previous_step":
      return (
        <WorkflowNodeSummary icon={CornerUpLeft}>
          {t(($) => $.canvas.routing.previous_step, {
            node: routing.from_node,
          })}
        </WorkflowNodeSummary>
      );
    case "capability":
      return (
        <WorkflowNodeSummary icon={Sparkles}>
          {t(($) => $.canvas.routing.capability, {
            capability: routing.capability,
          })}
        </WorkflowNodeSummary>
      );
    default:
      return (
        <WorkflowNodeSummary icon={Users}>
          {t(($) => $.canvas.routing.unknown, { strategy: routing.strategy })}
        </WorkflowNodeSummary>
      );
  }
}
