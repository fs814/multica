"use client";

import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { workflowInputInstanceListOptions, useSaveWorkflowInputInstance, useDeleteWorkflowInputInstance, type WorkflowInputInstance } from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction } from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../../i18n";

export function WorkflowInputInstances({ templateId, templateVersionId, values, projectId, disabled, onLoad }: {
  templateId: string;
  templateVersionId?: string;
  values: Record<string, string>;
  projectId: string | null;
  disabled: boolean;
  onLoad(input: Record<string, string> | null, projectId: string | null, versionId?: string | null): void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const { data: instances = [], isPending, isError } = useQuery(workflowInputInstanceListOptions(wsId, templateId));
  const save = useSaveWorkflowInputInstance(templateId);
  const remove = useDeleteWorkflowInputInstance(templateId);
  // Keep the revision we loaded, not a background-refetched revision. Otherwise
  // another window's edit could be overwritten with our older input values.
  const [selected, setSelected] = useState<WorkflowInputInstance | null>(null);
  const [name, setName] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const busy = disabled || save.isPending || remove.isPending;
  const saving = useRef(false);
  const saveInstance = async (replace: boolean) => {
    if (busy || saving.current || !name.trim()) return;
    saving.current = true;
    try {
      const instance = await save.mutateAsync({ name: name.trim(), input: { ...values }, projectId, ...(templateVersionId ? { templateVersionId } : {}),
        ...(replace && selected ? { id: selected.id, revision: selected.revision } : {}) });
      setSelected(instance);
      setName(instance.name);
      toast.success(t(($) => $.input_instances.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.input_instances.failed));
    } finally {
      saving.current = false;
    }
  };
  const deleteInstance = async () => {
    if (!selected) return;
    try {
      await remove.mutateAsync(selected.id);
      setSelected(null);
      setName("");
      setConfirmDelete(false);
      toast.success(t(($) => $.input_instances.deleted));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.input_instances.failed));
    }
  };
  return (
    <section className="flex flex-col gap-2 rounded-md border bg-muted/20 p-3">
      <span className="text-caption font-medium">{t(($) => $.input_instances.label)}</span>
      <p className="text-caption text-muted-foreground">{t(($) => $.input_instances.hint)}</p>
      <Select items={[{ value: "new", label: t(($) => $.input_instances.new_input) }, ...instances.map((instance) => ({ value: instance.id, label: instance.name }))]} value={selected?.id ?? "new"} disabled={busy || isPending || isError} onValueChange={(id) => {
        const instance = instances.find((item) => item.id === id);
        setSelected(instance ?? null);
        setName(instance?.name ?? "");
        onLoad(instance ? { ...instance.input } : null, instance?.projectId ?? null, instance?.templateVersionId);
      }}>
        <SelectTrigger className="w-full" aria-label={t(($) => $.input_instances.label)}><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value="new">{t(($) => $.input_instances.new_input)}</SelectItem>
          {instances.map((instance) => <SelectItem key={instance.id} value={instance.id}>{instance.name}</SelectItem>)}
        </SelectContent>
      </Select>
      {isError && <p role="alert" className="text-caption text-destructive">{t(($) => $.input_instances.load_failed)}</p>}
      <Input aria-label={t(($) => $.input_instances.name)} placeholder={t(($) => $.input_instances.name)} maxLength={100} value={name} disabled={busy} onChange={(event) => setName(event.target.value)} />
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" variant="outline" disabled={busy || isError || !name.trim()} onClick={() => void saveInstance(false)}>
          {selected ? t(($) => $.input_instances.save_as) : t(($) => $.input_instances.save)}
        </Button>
        {selected && <>
          <Button type="button" size="sm" variant="outline" disabled={busy || !name.trim()} onClick={() => void saveInstance(true)}>{t(($) => $.input_instances.update)}</Button>
          <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={() => setConfirmDelete(true)}>{t(($) => $.input_instances.delete)}</Button>
        </>}
      </div>
      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader><AlertDialogTitle>{t(($) => $.input_instances.delete)}</AlertDialogTitle><AlertDialogDescription>{t(($) => $.input_instances.delete_hint)}</AlertDialogDescription></AlertDialogHeader>
          <AlertDialogFooter><AlertDialogCancel>{t(($) => $.runs.dialog.cancel)}</AlertDialogCancel><AlertDialogAction disabled={remove.isPending} onClick={() => void deleteInstance()}>{t(($) => $.input_instances.delete)}</AlertDialogAction></AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}