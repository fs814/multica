"use client";

import { useState, type FormEvent } from "react";
import { AlertCircle, FilePenLine, Loader2, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { useCreateWorkflowTemplate } from "@multica/core/workflows";
import type { CreateWorkflowTemplateRequest } from "@multica/core/workflows";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { AiWorkflowBuilder } from "./ai-workflow-builder";

const WORKFLOW_KEY_RE = /^[a-z0-9][a-z0-9_-]*$/;

function keyFromName(name: string): string {
  return name
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^[_-]+|[_-]+$/g, "")
    .slice(0, 128)
    .replace(/[_-]+$/g, "");
}

export function CreateWorkflowDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("workflows");
  const wsPaths = useWorkspacePaths();
  const navigation = useNavigation();
  const createTemplate = useCreateWorkflowTemplate();
  const [mode, setMode] = useState<"manual" | "ai">("manual");
  const [aiBusy, setAiBusy] = useState(false);
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [keyTouched, setKeyTouched] = useState(false);
  const [description, setDescription] = useState("");
  const [formError, setFormError] = useState("");

  const trimmedName = name.trim();
  const trimmedKey = key.trim();
  const keyValid =
    trimmedKey.length <= 128 && WORKFLOW_KEY_RE.test(trimmedKey);

  const reset = () => {
    setMode("manual");
    setAiBusy(false);
    setName("");
    setKey("");
    setKeyTouched(false);
    setDescription("");
    setFormError("");
  };

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen && (createTemplate.isPending || aiBusy)) return;
    if (!nextOpen) reset();
    onOpenChange(nextOpen);
  };

  const createWorkflow = async (
    body: CreateWorkflowTemplateRequest,
  ): Promise<boolean> => {
    if (createTemplate.isPending) return false;
    setFormError("");
    try {
      const created = await createTemplate.mutateAsync(body);
      if (!created.id || !created.key) {
        setFormError(t(($) => $.page.create.unreadable));
        return false;
      }
      toast.success(t(($) => $.page.create.created));
      reset();
      onOpenChange(false);
      navigation.push(wsPaths.workflowDetail(created.id));
      return true;
    } catch (error) {
      setFormError(
        error instanceof Error
          ? error.message
          : t(($) => $.page.create.failed),
      );
      return false;
    }
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!trimmedName || !keyValid || createTemplate.isPending) return;
    await createWorkflow({
      key: trimmedKey,
      name: trimmedName,
      description: description.trim(),
      definition: {
        schema_version: 1,
        entry_node: "input",
        nodes: [
          {
            key: "input",
            type: "input",
            name: t(($) => $.page.create.default_input_name),
            next: ["end"],
            input_fields: [],
          },
          {
            key: "end",
            type: "end",
            name: t(($) => $.page.create.default_end_name),
            next: [],
          },
        ],
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.page.create.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.page.create.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="grid grid-cols-2 gap-1 rounded-lg bg-muted p-1">
          <Button
            type="button"
            size="sm"
            variant={mode === "manual" ? "default" : "ghost"}
            disabled={createTemplate.isPending || aiBusy}
            onClick={() => {
              setMode("manual");
              setFormError("");
            }}
          >
            <FilePenLine className="size-4" />
            {t(($) => $.page.create.manual_mode)}
          </Button>
          <Button
            type="button"
            size="sm"
            variant={mode === "ai" ? "default" : "ghost"}
            disabled={createTemplate.isPending || aiBusy}
            onClick={() => {
              setMode("ai");
              setFormError("");
            }}
          >
            <Sparkles className="size-4" />
            {t(($) => $.page.create.ai_mode)}
          </Button>
        </div>

        {mode === "manual" ? (
          <form onSubmit={handleSubmit}>
          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="create-workflow-name">
                {t(($) => $.page.create.name_label)}
              </Label>
              <Input
                id="create-workflow-name"
                autoFocus
                maxLength={200}
                value={name}
                placeholder={t(($) => $.page.create.name_placeholder)}
                onChange={(event) => {
                  const nextName = event.target.value;
                  setName(nextName);
                  if (!keyTouched) setKey(keyFromName(nextName));
                  setFormError("");
                }}
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="create-workflow-key">
                {t(($) => $.page.create.key_label)}
              </Label>
              <Input
                id="create-workflow-key"
                maxLength={128}
                value={key}
                spellCheck={false}
                className="font-mono"
                placeholder={t(($) => $.page.create.key_placeholder)}
                aria-describedby="create-workflow-key-hint"
                aria-invalid={Boolean(trimmedKey) && !keyValid}
                onChange={(event) => {
                  setKeyTouched(true);
                  setKey(event.target.value.toLowerCase());
                  setFormError("");
                }}
              />
              <p
                id="create-workflow-key-hint"
                className={
                  Boolean(trimmedKey) && !keyValid
                    ? "text-caption text-destructive"
                    : "text-caption text-muted-foreground"
                }
              >
                {Boolean(trimmedKey) && !keyValid
                  ? t(($) => $.page.create.key_invalid)
                  : t(($) => $.page.create.key_hint)}
              </p>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="create-workflow-description">
                {t(($) => $.page.create.description_label)}
              </Label>
              <Textarea
                id="create-workflow-description"
                rows={3}
                value={description}
                className="resize-none"
                placeholder={t(($) => $.page.create.description_placeholder)}
                onChange={(event) => {
                  setDescription(event.target.value);
                  setFormError("");
                }}
              />
            </div>

            {formError ? (
              <div
                role="alert"
                className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-body text-destructive"
              >
                <AlertCircle className="mt-0.5 size-4 shrink-0" />
                <span>{formError}</span>
              </div>
            ) : null}
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={createTemplate.isPending}
              onClick={() => handleOpenChange(false)}
            >
              {t(($) => $.page.create.cancel)}
            </Button>
            <Button
              type="submit"
              disabled={!trimmedName || !keyValid || createTemplate.isPending}
            >
              {createTemplate.isPending ? (
                <>
                  <Loader2 className="size-4 animate-spin" />
                  {t(($) => $.page.create.creating)}
                </>
              ) : (
                t(($) => $.page.create.submit)
              )}
            </Button>
          </DialogFooter>
          </form>
        ) : (
          <div className="space-y-4 pt-4">
            <AiWorkflowBuilder
              creating={createTemplate.isPending}
              onCreate={createWorkflow}
              onCancel={() => handleOpenChange(false)}
              onBusyChange={setAiBusy}
            />
            {formError ? (
              <div
                role="alert"
                className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-body text-destructive"
              >
                <AlertCircle className="mt-0.5 size-4 shrink-0" />
                <span>{formError}</span>
              </div>
            ) : null}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
