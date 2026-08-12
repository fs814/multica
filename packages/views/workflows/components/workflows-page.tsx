"use client";

import { useState } from "react";
import { AlertCircle, Plus, Workflow } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { workflowTemplateListOptions } from "@multica/core/workflows";
import type { WorkflowTemplate } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
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
import { useRowLink } from "../../navigation";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { CreateWorkflowDialog } from "./create-workflow-dialog";
import { WorkflowStatusBadge } from "./workflow-status-badge";
import { useT } from "../../i18n";

// Column template — same conventions as the squads list (the simplest member
// of the ListGrid family: subgrid template, fixed tracks, two-zone container
// responsiveness, no virtualization because a workspace holds a handful of
// templates, not thousands).
//
// Core set below @2xl is name + status: the key is a machine identifier and
// version/step counts are secondary, so they are the columns that drop.
const GRID_COLS =
  "grid-cols-[0.75rem_minmax(120px,1fr)_6rem_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_minmax(200px,1fr)_12rem_6rem_6rem_4.5rem_0.75rem]";

// Fixed tracks (edges 12+12, name min 200, key 192, status 96, version 96,
// nodes 72) plus the 6 gap-x-3 gaps between the wide template's 7 tracks.
// Exposed as a CSS var so the min-width and the track list derive from the
// same numbers — Tailwind only sees literal classes, so an interpolated
// `min-w-[...]` would never be generated.
const TRACK_VARS = {
  "--wfc-minw": `${24 + 200 + 192 + 96 + 96 + 72 + 6 * 12}px`,
} as React.CSSProperties;

function WorkflowListHeader() {
  const { t } = useT("workflows");
  return (
    <ListGridHeader>
      <ListGridHeaderCell>{t(($) => $.page.table.name)}</ListGridHeaderCell>
      <ListGridHeaderCell className="hidden @2xl:flex">
        {t(($) => $.page.table.key)}
      </ListGridHeaderCell>
      <ListGridHeaderCell>{t(($) => $.page.table.status)}</ListGridHeaderCell>
      <ListGridHeaderCell className="hidden @2xl:flex">
        {t(($) => $.page.table.version)}
      </ListGridHeaderCell>
      <ListGridHeaderCell className="hidden @2xl:flex" align="right">
        {t(($) => $.page.table.nodes)}
      </ListGridHeaderCell>
    </ListGridHeader>
  );
}

function NameCell({ template }: { template: WorkflowTemplate }) {
  const { t } = useT("workflows");
  return (
    <ListGridCell className="gap-2">
      <div className="min-w-0 flex-1">
        <span className="block min-w-0 truncate text-sm font-medium">
          {template.name}
        </span>
        {template.description ? (
          <span className="block min-w-0 truncate text-xs text-muted-foreground">
            {template.description}
          </span>
        ) : null}
      </div>
      {/* A built-in template is seeded, not authored, so a reader needs to
          know they did not create it (and cannot expect to own its graph). */}
      {template.is_builtin ? (
        <Badge variant="secondary" className="shrink-0">
          {t(($) => $.page.builtin_badge)}
        </Badge>
      ) : null}
    </ListGridCell>
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
          <ListGridHeaderCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-10" />
          </ListGridHeaderCell>
          <ListGridHeaderCell>
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-10" />
          </ListGridHeaderCell>
        </ListGridHeader>
        {Array.from({ length: 4 }).map((_, i) => (
          <ListGridRow key={i} className="hover:bg-transparent">
            <ListGridCell>
              <Skeleton className="h-3.5 w-40 max-w-full" />
            </ListGridCell>
            <ListGridCell className="hidden @2xl:flex">
              <Skeleton className="h-3 w-24" />
            </ListGridCell>
            <ListGridCell>
              <Skeleton className="h-5 w-16 rounded-full" />
            </ListGridCell>
            <ListGridCell className="hidden @2xl:flex">
              <Skeleton className="h-3 w-8" />
            </ListGridCell>
            <ListGridCell className="hidden justify-end @2xl:flex">
              <Skeleton className="h-3 w-6" />
            </ListGridCell>
          </ListGridRow>
        ))}
      </ListGrid>
    </div>
  );
}

export function WorkflowsPage() {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const [createOpen, setCreateOpen] = useState(false);
  const rowLink = useRowLink();

  const {
    data: templates = [],
    isLoading,
    error: listError,
    refetch,
  } = useQuery(workflowTemplateListOptions(wsId));

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <CollectionPageHeader
        icon={Workflow}
        title={t(($) => $.page.title)}
        count={templates.length}
        actions={
          <CollectionPageHeaderAction
            icon={Plus}
            label={t(($) => $.page.create.action)}
            onClick={() => setCreateOpen(true)}
          />
        }
      />

      {listError ? (
        <CollectionPageState
          role="alert"
          tone="destructive"
          icon={AlertCircle}
          title={t(($) => $.page.error_title)}
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
              {t(($) => $.page.retry)}
            </Button>
          }
        />
      ) : isLoading ? (
        <LoadingSkeleton />
      ) : templates.length === 0 ? (
        // The list endpoint seeds the built-in template before answering, so
        // an empty list means the workspace really has none — not that the
        // seeder has yet to run.
        <CollectionPageState
          icon={Workflow}
          title={t(($) => $.page.empty.title)}
          description={t(($) => $.page.empty.hint)}
          actions={
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setCreateOpen(true)}
            >
              <Plus className="size-4" />
              {t(($) => $.page.create.create_first)}
            </Button>
          }
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto @container">
          <ListGrid
            className={`${GRID_COLS} @2xl:min-w-[var(--wfc-minw)]`}
            style={{
              ...TRACK_VARS,
              paddingBottom: LIST_GRID_BOTTOM_CLEARANCE,
            }}
          >
            <WorkflowListHeader />
            {templates.map((template) => (
              <ListGridRow
                key={template.id}
                className="cursor-pointer"
                {...rowLink(wsPaths.workflowDetail(template.id))}
              >
                <NameCell template={template} />
                <ListGridCell className="hidden @2xl:flex">
                  <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">
                    {template.key}
                  </span>
                </ListGridCell>
                <ListGridCell>
                  <WorkflowStatusBadge status={template.status} />
                </ListGridCell>
                <ListGridCell className="hidden @2xl:flex">
                  <span className="text-xs tabular-nums text-muted-foreground">
                    {/* No current version = never published. Shown as a dash
                        rather than "v0", which would imply a real version. */}
                    {template.current_version === null
                      ? t(($) => $.page.no_version)
                      : t(($) => $.detail.versions.label, {
                          version: template.current_version,
                        })}
                  </span>
                </ListGridCell>
                <ListGridCell className="hidden justify-end @2xl:flex">
                  <span className="text-xs tabular-nums text-muted-foreground">
                    {template.node_count}
                  </span>
                </ListGridCell>
              </ListGridRow>
            ))}
          </ListGrid>
        </div>
      )}
      <CreateWorkflowDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
