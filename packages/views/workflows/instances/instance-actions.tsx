"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  workflowTemplateDetailOptions,
  workflowTemplateListOptions,
  workflowTemplateRunOptions,
  type WorkflowInputInstance,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Archive, ArchiveRestore, ChevronDown, Copy, Ellipsis, Loader2, Plus } from "@multica/ui/icons";
import { useT } from "../../i18n";
import { CreateInstanceDialog } from "./create-instance-dialog";

/**
 * One word for where an instance stands, in the order a person has to deal
 * with it: archived beats everything, an unbound instance can't run whatever
 * its inputs say, and only then do the server's readiness checks matter.
 */
export type InstanceStatus = "archived" | "unbound" | "ready" | "needs_input" | "checking";

export function instanceStatus(row: Pick<WorkflowInputInstance, "archivedAt" | "templateVersionId">, ready: boolean | undefined): InstanceStatus {
  if (row.archivedAt) return "archived";
  if (!row.templateVersionId) return "unbound";
  if (ready === undefined) return "checking";
  return ready ? "ready" : "needs_input";
}

const STATUS_DOT: Record<InstanceStatus, string> = {
  ready: "bg-emerald-500",
  needs_input: "bg-amber-500",
  unbound: "bg-amber-500",
  archived: "bg-muted-foreground/50",
  checking: "bg-muted-foreground/30",
};

export function InstanceStatusChip({ status }: { status: InstanceStatus }) {
  const { t } = useT("workflows");
  if (status === "checking") return null;
  const label = {
    ready: t(($) => $.instances.status_ready),
    needs_input: t(($) => $.instances.status_needs_input),
    unbound: t(($) => $.instances.status_unbound),
    archived: t(($) => $.instances.archived),
  }[status];
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-caption text-muted-foreground">
      <span aria-hidden className={`size-1.5 rounded-full ${STATUS_DOT[status]}`} />
      {label}
    </span>
  );
}

/**
 * The secondary actions of an instance behind one "…" button, so a row or a
 * page header shows a single primary action (Run). Archiving is reversible
 * (Restore), which is why it needs no confirmation: the server's "delete" is
 * the same archive, so there is only this one way to put an instance away.
 */
export function InstanceMenu({ archived, disabled, archiveDisabled, onDuplicate, onArchive }: {
  archived: boolean;
  disabled?: boolean;
  archiveDisabled?: boolean;
  onDuplicate: () => void;
  onArchive: () => void;
}) {
  const { t } = useT("workflows");
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button size="icon-sm" variant="ghost" disabled={disabled} aria-label={t(($) => $.instances.more)} title={t(($) => $.instances.more)}>
            <Ellipsis />
          </Button>
        }
      />
      <DropdownMenuContent align="end">
        <DropdownMenuItem onClick={onDuplicate}>
          <Copy className="size-3.5" />
          {t(($) => $.instances.duplicate)}
        </DropdownMenuItem>
        <DropdownMenuItem disabled={archiveDisabled} onClick={onArchive}>
          {archived ? <ArchiveRestore className="size-3.5" /> : <Archive className="size-3.5" />}
          {archived ? t(($) => $.instances.restore) : t(($) => $.instances.archive)}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * "New instance" from anywhere: with a workflow given (its own Instances
 * section) it opens the form directly; otherwise it first asks which
 * workflow. The form needs the workflow's saved draft and its current
 * publication, which load once a workflow is chosen.
 */
export function NewInstanceButton({ templateId: fixed }: { templateId?: string }) {
  const { t } = useT("workflows");
  const ws = useWorkspaceId();
  const templates = useQuery({ ...workflowTemplateListOptions(ws), enabled: !fixed });
  const [chosen, setChosen] = useState<string | null>(null);
  const choices = (templates.data ?? []).filter((tpl) => tpl.status !== "archived");
  if (fixed) {
    return <>
      <Button size="sm" onClick={() => setChosen(fixed)}><Plus />{t(($) => $.instances.new)}</Button>
      {chosen && <NewInstanceLoader templateId={chosen} onClose={() => setChosen(null)} />}
    </>;
  }
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button size="sm"><Plus />{t(($) => $.instances.new)}<ChevronDown className="size-3" /></Button>}
        />
        <DropdownMenuContent align="end" className="max-h-80 w-64 overflow-y-auto">
          <DropdownMenuGroup>
            <DropdownMenuLabel>{t(($) => $.instances.new_pick)}</DropdownMenuLabel>
            {choices.map((tpl) => (
              <DropdownMenuItem key={tpl.id} onClick={() => setChosen(tpl.id)}>
                <span className="truncate">{tpl.name}</span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      {chosen && <NewInstanceLoader templateId={chosen} onClose={() => setChosen(null)} />}
    </>
  );
}

function NewInstanceLoader({ templateId, onClose }: { templateId: string; onClose: () => void }) {
  const { t } = useT("workflows");
  const ws = useWorkspaceId();
  const detail = useQuery(workflowTemplateDetailOptions(ws, templateId));
  const hasPublished = Boolean(detail.data?.versions.some((v) => v.status === "published"));
  const published = useQuery({
    ...workflowTemplateRunOptions(ws, templateId, detail.data?.current_version),
    enabled: hasPublished,
  });
  if (detail.error) return <p role="alert" className="text-caption text-destructive">{detail.error.message}</p>;
  if (!detail.data || (hasPublished && published.isPending)) {
    return <span role="status" className="inline-flex items-center gap-1.5 text-caption text-muted-foreground"><Loader2 className="size-3.5 animate-spin" />{t(($) => $.instances.loading_workflow)}</span>;
  }
  const versionId = published.data?.versions.find((v) => v.status === "published" && v.version === published.data?.current_version)?.id;
  return (
    <CreateInstanceDialog
      templateId={templateId}
      templateName={detail.data.name}
      definition={detail.data.definition}
      publishedDefinition={published.data?.definition}
      versionId={versionId}
      open
      onOpenChange={(open) => { if (!open) onClose(); }}
    />
  );
}
