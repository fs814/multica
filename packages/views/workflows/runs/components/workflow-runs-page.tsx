"use client";

import { AlertCircle, Play } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { workflowRunPageOptions } from "@multica/core/workflows";
import type { WorkflowRun } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  LIST_GRID_BOTTOM_CLEARANCE,
  ListGrid,
  ListGridCell,
  ListGridHeader,
  ListGridHeaderCell,
  ListGridRow,
} from "@multica/ui/components/ui/list-grid";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useRowLink } from "../../../navigation";
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

// Core set below @2xl is status + workflow: the run has no name of its own, so
// the template is its identity, and everything else is detail a narrow pane can
// defer to the detail page.
const GRID_COLS =
  "grid-cols-[0.75rem_7rem_minmax(120px,1fr)_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_7rem_minmax(180px,1fr)_10rem_8rem_6rem_0.75rem]";

// Fixed tracks (edges 12+12, status 112, workflow min 180, current step 160,
// started 128, duration 96) plus the 6 gap-x-3 gaps between the wide
// template's 7 tracks. A CSS var so the min-width and the track list derive
// from the same numbers - Tailwind only sees literal classes.
const TRACK_VARS = {
  "--wfr-minw": `${24 + 112 + 180 + 160 + 128 + 96 + 6 * 12}px`,
} as React.CSSProperties;

function RunsListHeader() {
  const { t } = useT("workflows");
  return (
    <ListGridHeader>
      <ListGridHeaderCell>
        {t(($) => $.runs.page.table.status)}
      </ListGridHeaderCell>
      <ListGridHeaderCell>
        {t(($) => $.runs.page.table.workflow)}
      </ListGridHeaderCell>
      <ListGridHeaderCell className="hidden @2xl:flex">
        {t(($) => $.runs.page.table.current_node)}
      </ListGridHeaderCell>
      <ListGridHeaderCell className="hidden @2xl:flex">
        {t(($) => $.runs.page.table.started)}
      </ListGridHeaderCell>
      <ListGridHeaderCell className="hidden @2xl:flex" align="right">
        {t(($) => $.runs.page.table.duration)}
      </ListGridHeaderCell>
    </ListGridHeader>
  );
}

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

function LoadingSkeleton() {
  return (
    <div className="min-h-0 flex-1 overflow-auto @container">
      <ListGrid className={GRID_COLS}>
        <ListGridHeader>
          <ListGridHeaderCell>
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
          <ListGridHeaderCell>
            <Skeleton className="h-3 w-16" />
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-16" />
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
        </ListGridHeader>
        {Array.from({ length: 5 }).map((_, i) => (
          <ListGridRow key={i} className="hover:bg-transparent">
            <ListGridCell>
              <Skeleton className="h-5 w-16 rounded-full" />
            </ListGridCell>
            <ListGridCell>
              <Skeleton className="h-3.5 w-40 max-w-full" />
            </ListGridCell>
            <ListGridCell className="hidden @2xl:flex">
              <Skeleton className="h-3 w-24" />
            </ListGridCell>
            <ListGridCell className="hidden @2xl:flex">
              <Skeleton className="h-3 w-20" />
            </ListGridCell>
            <ListGridCell className="hidden justify-end @2xl:flex">
              <Skeleton className="h-3 w-10" />
            </ListGridCell>
          </ListGridRow>
        ))}
      </ListGrid>
    </div>
  );
}

export function WorkflowRunsPage({ templateId }: { templateId?: string } = {}) {
  const { t, i18n } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const rowLink = useRowLink();
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
          count={total}
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
      ) : isLoading ? (
        <LoadingSkeleton />
      ) : runs.length === 0 ? (
        <CollectionPageState
          icon={Play}
          title={t(($) => $.runs.page.empty.title)}
          description={t(($) => $.runs.page.empty.hint)}
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto @container">
          <ListGrid
            className={`${GRID_COLS} @2xl:min-w-[var(--wfr-minw)]`}
            style={{
              ...TRACK_VARS,
              paddingBottom: LIST_GRID_BOTTOM_CLEARANCE,
            }}
          >
            <RunsListHeader />
            {runs.map((run) => (
              <ListGridRow
                key={run.id}
                className="cursor-pointer"
                {...rowLink(
                  wsPaths.workflowRunDetail(run.id) +
                    "?return_to=" +
                    encodeURIComponent(location.current),
                )}
              >
                <ListGridCell>
                  <WorkflowRunStatusBadge status={run.status} />
                </ListGridCell>
                <ListGridCell>
                  <div className="min-w-0 flex-1">
                    <span className="block min-w-0 truncate text-body font-medium">
                      {run.template_name}
                    </span>
                    <span className="block min-w-0 truncate font-mono text-caption text-muted-foreground">
                      {run.template_key}
                    </span>
                  </div>
                </ListGridCell>
                <ListGridCell className="hidden @2xl:flex">
                  {/* A terminal run has no current node, and a dash is the
                      honest rendering: naming the last node it touched would
                      read as "still there". */}
                  <span className="min-w-0 truncate font-mono text-caption text-muted-foreground">
                    {run.current_node_key ?? t(($) => $.runs.page.not_started)}
                  </span>
                </ListGridCell>
                <ListGridCell className="hidden @2xl:flex">
                  <span className="text-caption tabular-nums text-muted-foreground">
                    {run.started_at
                      ? formatInTimeZone(
                          run.started_at,
                          undefined,
                          i18n.language,
                        )
                      : t(($) => $.runs.page.not_started)}
                  </span>
                </ListGridCell>
                <ListGridCell className="hidden justify-end @2xl:flex">
                  <DurationCell run={run} />
                </ListGridCell>
              </ListGridRow>
            ))}
          </ListGrid>
        </div>
      )}
    </div>
  );
}
