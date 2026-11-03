"use client";

import { AlertCircle, Play } from "@multica/ui/icons";
import { useQuery } from "@tanstack/react-query";
import { workflowRunPageOptions } from "@multica/core/workflows";
import type { WorkflowRun } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { CollectionList } from "../../../layout/collection-list";
import { formatInTimeZone } from "../../../common/format-in-time-zone";
import { formatDuration } from "../../../dashboard/utils";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../../../layout/collection-page";
import { useT } from "../../../i18n";
import { runElapsedSeconds } from "../run-reason";
import {
  useWorkflowLocation,
  workflowListOffset,
} from "../../use-workflow-location";
import { WorkflowRunStatusBadge } from "./run-status-badge";

/**
 * The runs list: every execution in the workspace, newest first (the server
 * orders it).
 *
 * Column choice follows the one question a reader opens this page with - "is
 * anything stuck, and for how long". Status is first and never drops, because
 * `blocked` and `waiting_acceptance` are the two states where nothing happens
 * until a person acts. Current step is what turns "running" into something
 * actionable; duration is what turns it into something *urgent*.
 *
 * No virtualization, matching the templates list: this endpoint is paginated
 * server-side (`limit`/`offset`) rather than unbounded, so the DOM cost is
 * capped by the page size.
 */

/**
 * Elapsed time for one row.
 *
 * A still-running run is labelled "N so far" rather than shown bare: a bare
 * number next to a finished run would read as a final duration, and the whole
 * point of this column is telling a nine-minute run apart from a nine-minute
 * *stall*. Recomputed on render rather than ticked on a timer - the realtime
 * invalidation already re-renders this list on every engine event, and a
 * per-row interval would re-render the whole page once a second for a number
 * nobody reads to the second.
 */
function DurationCell({ run }: { run: WorkflowRun }) {
  const { t } = useT("workflows");
  const seconds = runElapsedSeconds(run.started_at, run.completed_at);
  if (seconds === null) {
    return (
      <span className="text-caption tabular-nums text-muted-foreground">
        {t(($) => $.runs.page.not_started)}
      </span>
    );
  }
  const formatted = formatDuration(seconds, "<1m");
  return (
    <span className="text-caption tabular-nums text-muted-foreground">
      {run.completed_at
        ? formatted
        : t(($) => $.runs.page.duration_running, { duration: formatted })}
    </span>
  );
}

export function WorkflowRunsPage({ templateId }: { templateId?: string } = {}) {
  const { t, i18n } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const location = useWorkflowLocation();
  const status = location.params.get("status") ?? "";
  const offset = workflowListOffset(location.params.get("run_offset"));

  const {
    data,
    isLoading,
    error: listError,
    refetch,
  } = useQuery(
    workflowRunPageOptions(wsId, {
      template_id: templateId,
      status: status || undefined,
      limit: 30,
      offset,
    }),
  );

  const runs = data?.runs ?? [];
  const total = data?.total ?? 0;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {!templateId && (
        <CollectionPageHeader
          icon={Play}
          title={t(($) => $.runs.page.title)}
          />
      )}
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2">
        <select
          className="rounded border bg-background p-2 text-caption"
          aria-label={t(($) => $.runs.page.table.status)}
          value={status}
          onChange={(e) =>
            location.update({ status: e.target.value, run_offset: 0 })
          }
        >
          <option value="">{t(($) => $.runs.page.all_statuses)}</option>
          {[
            "pending",
            "running",
            "waiting_acceptance",
            "blocked",
            "completed",
            "failed",
            "cancelled",
          ].map((value) => (
            <option key={value} value={value}>
              {value}
            </option>
          ))}
        </select>
        <span className="text-caption text-muted-foreground">{total}</span>
        <Button
          variant="outline"
          disabled={offset === 0 || isLoading}
          onClick={() =>
            location.update({ run_offset: Math.max(0, offset - 30) })
          }
        >
          {t(($) => $.instances.previous)}
        </Button>
        <Button
          variant="outline"
          disabled={offset + 30 >= total || isLoading}
          onClick={() => location.update({ run_offset: offset + 30 })}
        >
          {t(($) => $.instances.next)}
        </Button>
      </div>

      {listError ? (
        <CollectionPageState
          role="alert"
          tone="destructive"
          icon={AlertCircle}
          title={t(($) => $.runs.page.error_title)}
          description={
            listError instanceof Error ? listError.message : String(listError)
          }
          actions={
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => refetch()}
            >
              {t(($) => $.runs.page.retry)}
            </Button>
          }
        />
      ) : !isLoading && runs.length === 0 ? (
        <CollectionPageState
          icon={Play}
          title={t(($) => $.runs.page.empty.title)}
          description={t(($) => $.runs.page.empty.hint)}
        />
      ) : (
<CollectionList label={t(($) => $.runs.page.title)} rows={runs} loading={isLoading} getRowId={(run)=>run.id} getRowHref={(run)=>wsPaths.workflowRunDetail(run.id)+"?return_to="+encodeURIComponent(location.current)} getRowTitle={(run)=>run.template_name} columns={[
      {key:"name", header:t(($) => $.runs.page.table.workflow), cell:(run)=><span>{run.template_name}<span className="text-muted-foreground"> {run.template_key}</span></span>},
      {key:"status", header:t(($) => $.runs.page.table.status), cell:(run)=><WorkflowRunStatusBadge status={run.status} />, width:120, core:true},
      {key:"node", header:t(($) => $.runs.page.table.current_node), cell:(run)=>run.current_node_key ?? t(($) => $.runs.page.not_started), width:160},
      {key:"started", header:t(($) => $.runs.page.table.started), cell:(run)=>run.started_at ? formatInTimeZone(run.started_at, undefined, i18n.language) : t(($) => $.runs.page.not_started), width:160},
      {key:"duration", header:t(($) => $.runs.page.table.duration), cell:(run)=><DurationCell run={run}/>, width:100}
    ]} />
      )}
    </div>
  );
}
