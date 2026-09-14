"use client";

import { useRef, useState } from "react";
import { useDeleteWorkflowInputInstance, type WorkflowInputInstance } from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle,
  AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction,
} from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

export function DeleteInstanceButton({ row, disabled = false, onDeleted }: {
  row: WorkflowInputInstance;
  disabled?: boolean;
  onDeleted?(): void;
}) {
  const { t } = useT("workflows");
  const remove = useDeleteWorkflowInputInstance(row.templateId);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const busy = useRef(false);
  const confirm = async () => {
    if (busy.current || disabled) return;
    busy.current = true;
    setError("");
    try {
      await remove.mutateAsync(row.id);
      setOpen(false);
      onDeleted?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      busy.current = false;
    }
  };
  if (row.archivedAt) return null;
  return (
    <>
      <Button size="sm" variant="outline" className="text-destructive"
        disabled={disabled || remove.isPending}
        onClick={() => { setError(""); setOpen(true); }}>
        {t(($) => $.input_instances.delete)}
      </Button>
      <AlertDialog open={open} onOpenChange={(next) => { if (!busy.current) setOpen(next); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.input_instances.delete)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.instances.delete_confirmation, { name: row.name })}</AlertDialogDescription>
          </AlertDialogHeader>
          {error && <p role="alert" className="text-caption text-destructive">{error}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={remove.isPending}>{t(($) => $.instances.cancel)}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={disabled || remove.isPending} onClick={() => void confirm()}>
              {t(($) => $.input_instances.delete)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
