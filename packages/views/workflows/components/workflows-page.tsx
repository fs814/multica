"use client";
import { AppLink } from "../../navigation";

import { useState } from "react";
import { AlertCircle, Plus, Workflow } from "@multica/ui/icons";
import { useQuery } from "@tanstack/react-query";
import { workflowTemplateListOptions } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { CollectionList } from "../../layout/collection-list";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../../layout/collection-page";
import { CreateWorkflowDialog } from "./create-workflow-dialog";
import { WorkflowStatusBadge } from "./workflow-status-badge";
import { useT } from "../../i18n";

export function WorkflowsPage() {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const [createOpen, setCreateOpen] = useState(false);

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
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}><Plus />{t(($) => $.page.create.action)}</Button>
        }
      />

      <div className="border-b px-4 py-2"><AppLink href={wsPaths.workflowInstances()}>{t($=>$.instances.all)}</AppLink></div>
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
      ) : !isLoading && templates.length === 0 ? (
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
<CollectionList label={t(($) => $.page.title)} rows={templates} loading={isLoading} getRowId={(item) => item.id} getRowHref={(item) => wsPaths.workflowDetail(item.id)} getRowTitle={(item) => item.name} columns={[
      {key:"name", header:t(($) => $.page.table.name), cell:(item)=><span>{item.name} {item.is_builtin && <Badge variant="secondary">{t(($) => $.page.builtin_badge)}</Badge>}<span className="text-muted-foreground"> {item.description}</span></span>},
      {key:"key", header:t(($) => $.page.table.key), cell:(item)=>item.key, width:180},
      {key:"status", header:t(($) => $.page.table.status), cell:(item)=><WorkflowStatusBadge status={item.status} />, width:100, core:true},
      {key:"version", header:t(($) => $.page.table.version), cell:(item)=>item.current_version === null ? t(($) => $.page.no_version) : t(($) => $.detail.versions.label, {version:item.current_version}), width:100},
      {key:"nodes", header:t(($) => $.page.table.nodes), cell:(item)=>item.node_count, width:80}
    ]} />
      )}
      <CreateWorkflowDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
