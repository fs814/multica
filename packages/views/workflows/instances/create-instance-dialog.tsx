"use client";
import { useRef, useState } from "react";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  useSaveWorkflowInputInstance,
  workflowRunInputDefaults,
} from "@multica/core/workflows";
import type {
  WorkflowDefinition,
  SaveWorkflowInputInstance,
} from "@multica/core/workflows";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { inputNode, inputSchema, InstanceFields } from "./instance-form";

export function CreateInstanceDialog({
  templateId,
  templateName,
  definition,
  publishedDefinition,
  versionId,
  open,
  onOpenChange,
}: {
  templateId: string;
  templateName: string;
  definition: WorkflowDefinition;
  publishedDefinition?: WorkflowDefinition;
  versionId?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const pending = useRef(false);
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!pending.current) onOpenChange(next);
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        {open && (
          <CreateInstanceForm
            templateId={templateId}
            templateName={templateName}
            definition={definition}
            publishedDefinition={publishedDefinition}
            versionId={versionId}
            onPendingChange={(next) => {
              pending.current = next;
            }}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
function CreateInstanceForm({
  templateId,
  templateName,
  definition,
  publishedDefinition,
  versionId,
  onClose,
  onPendingChange,
}: {
  templateId: string;
  templateName: string;
  definition: WorkflowDefinition;
  publishedDefinition?: WorkflowDefinition;
  versionId?: string;
  onClose: () => void;
  onPendingChange: (pending: boolean) => void;
}) {
  const { t } = useT("workflows");
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const node = inputNode(definition);
  const [value, setValue] = useState<SaveWorkflowInputInstance>(() => ({
    name: templateName,
    input: workflowRunInputDefaults(definition, templateName),
    projectId: null,
    description: "",
    inputNode: node,
    imageAttachmentId: node?.image_attachment_id ?? "",
    templateVersionId:
      versionId &&
      inputSchema(node) === inputSchema(inputNode(publishedDefinition))
        ? versionId
        : null,
  }));
  const [uploading, setUploading] = useState(false);
  const save = useSaveWorkflowInputInstance(templateId);
  const busy = useRef(false);
  const attempt = useRef({ body: "", key: "" });
  const create = async () => {
    if (busy.current) return;
    busy.current = true;
    onPendingChange(true);
    const body = JSON.stringify(value);
    if (attempt.current.body !== body)
      attempt.current = { body, key: crypto.randomUUID() };
    try {
      const created = await save.mutateAsync({
        ...value,
        idempotencyKey: attempt.current.key,
      });
      onClose();
      navigation.push(paths.workflowInstanceDetail(created.id));
    } catch {
      // The mutation error is rendered below; preserve values and the retry key.
    } finally {
      busy.current = false;
      onPendingChange(false);
    }
  };
  return (
    <>
      <DialogTitle>{t(($) => $.instances.create)}</DialogTitle>
      <DialogDescription>{t(($) => $.instances.create_hint)}</DialogDescription>
      {!value.templateVersionId && (
        <p role="status" className="rounded border p-3 text-caption">
          {t(($) => $.instances.unbound)}
        </p>
      )}
      <InstanceFields
        value={value}
        onChange={setValue}
        disabled={save.isPending}
        onUploadingChange={(next) => {
          setUploading(next);
          onPendingChange(next);
        }}
      />
      {save.error && (
        <p role="alert" className="text-destructive">
          {save.error.message}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button
          variant="outline"
          onClick={onClose}
          disabled={save.isPending || uploading}
        >
          {t(($) => $.instances.cancel)}
        </Button>
        <Button
          disabled={!value.name.trim() || save.isPending || uploading}
          onClick={create}
        >
          {t(($) => $.instances.create_action)}
        </Button>
      </div>
    </>
  );
}
