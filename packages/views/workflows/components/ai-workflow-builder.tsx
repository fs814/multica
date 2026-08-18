"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { AlertCircle, Loader2, Sparkles } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { isRuntimeUsableForUser, runtimeListOptions } from "@multica/core/runtimes";
import type { CreateWorkflowTemplateRequest } from "@multica/core/workflows";
import {
  encodeWorkflowBuilderInput,
  encodeWorkflowBuilderRepairInput,
  parseWorkflowBuilderDraft,
  type WorkflowBuilderDraft,
} from "@multica/core/workflows";
import { agentListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

const POLL_INTERVAL_MS = 1500;
const GENERATION_TIMEOUT_MS = 180_000;
const WORKFLOW_KEY_RE = /^[a-z0-9][a-z0-9_-]*$/;

type Props = {
  creating: boolean;
  onCreate: (draft: CreateWorkflowTemplateRequest) => Promise<boolean>;
  onCancel: () => void;
  onBusyChange: (busy: boolean) => void;
};

export function AiWorkflowBuilder({
  creating,
  onCreate,
  onCancel,
  onBusyChange,
}: Props) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);
  const { data: runtimes = [], isLoading: runtimesLoading } = useQuery(
    runtimeListOptions(wsId),
  );
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const codexRuntimes = useMemo(
    () =>
      runtimes.filter(
        (runtime) =>
          runtime.provider === "codex" &&
          runtime.status === "online" &&
          isRuntimeUsableForUser(runtime, currentUserId),
      ),
    [currentUserId, runtimes],
  );
  const [runtimeId, setRuntimeId] = useState("");
  const [request, setRequest] = useState("");
  const [draft, setDraft] = useState<WorkflowBuilderDraft | null>(null);
  const [generating, setGenerating] = useState(false);
  const [error, setError] = useState("");
  const generationRef = useRef(0);
  const sessionRef = useRef("");
  const draftKeyValid =
    draft !== null &&
    draft.key.length <= 128 &&
    WORKFLOW_KEY_RE.test(draft.key.trim());

  useEffect(() => {
    if (
      runtimeId &&
      codexRuntimes.some((runtime) => runtime.id === runtimeId)
    ) {
      return;
    }
    setRuntimeId(codexRuntimes[0]?.id ?? "");
  }, [codexRuntimes, runtimeId]);

  useEffect(
    () => () => {
      generationRef.current += 1;
      const sessionId = sessionRef.current;
      if (sessionId) void api.deleteChatSession(sessionId).catch(() => {});
    },
    [],
  );

  const availableAgents = useMemo(() => {
    const codexRuntimeIds = new Set(
      runtimes
        .filter((runtime) => runtime.provider === "codex")
        .map((runtime) => runtime.id),
    );
    return agents
      .filter(
        (agent) =>
          !agent.archived_at && codexRuntimeIds.has(agent.runtime_id ?? ""),
      )
      .map((agent) => ({
        id: agent.id,
        name: agent.name,
        description: agent.description ?? "",
      }));
  }, [agents, runtimes]);

  const finishGeneration = (generation: number) => {
    if (generation !== generationRef.current) return;
    setGenerating(false);
    onBusyChange(false);
  };

  const generate = async () => {
    const prompt = request.trim();
    if (!prompt || !runtimeId || generating) return;

    const generation = generationRef.current + 1;
    generationRef.current = generation;
    setGenerating(true);
    onBusyChange(true);
    setError("");
    setDraft(null);
    let sessionId = "";
    let repairRequested = false;
    try {
      const session = await api.createWorkflowBuilderSession({
        runtime_id: runtimeId,
      });
      sessionId = session.session_id;
      sessionRef.current = sessionId;
      if (!sessionId) {
        throw new Error(t(($) => $.page.create.ai.start_failed));
      }
      await api.sendChatMessage(
        sessionId,
        encodeWorkflowBuilderInput(prompt, availableAgents),
      );

      const deadline = Date.now() + GENERATION_TIMEOUT_MS;
      while (Date.now() < deadline && generation === generationRef.current) {
        const [messages, pending] = await Promise.all([
          api.listChatMessages(sessionId),
          api.getPendingChatTask(sessionId),
        ]);
        const assistantMessages = [...messages]
          .reverse()
          .filter((message) => message.role === "assistant");
        const generated = assistantMessages
          .map((message) => parseWorkflowBuilderDraft(message.content))
          .find((value): value is WorkflowBuilderDraft => value !== null);
        if (generated) {
          const validation = await api.validateWorkflowDefinition(
            generated.definition,
          );
          if (!validation.valid) {
            throw new Error(
              validation.messages.join("\n") ||
                t(($) => $.page.create.ai.invalid_draft),
            );
          }
          if (generation === generationRef.current) setDraft(generated);
          return;
        }
        if (!pending.task_id) {
          if (assistantMessages.length > 0 && !repairRequested) {
            repairRequested = true;
            await api.sendChatMessage(
              sessionId,
              encodeWorkflowBuilderRepairInput(prompt, availableAgents),
            );
            continue;
          }
          throw new Error(t(($) => $.page.create.ai.no_draft));
        }
        await delay(POLL_INTERVAL_MS);
      }
      if (generation === generationRef.current) {
        throw new Error(t(($) => $.page.create.ai.timeout));
      }
    } catch (cause) {
      if (generation === generationRef.current) {
        setError(
          cause instanceof Error
            ? cause.message
            : t(($) => $.page.create.ai.generate_failed),
        );
      }
    } finally {
      if (sessionId) {
        void api.deleteChatSession(sessionId).catch(() => {});
        if (sessionRef.current === sessionId) sessionRef.current = "";
      }
      finishGeneration(generation);
    }
  };

  const stopGeneration = () => {
    generationRef.current += 1;
    const sessionId = sessionRef.current;
    sessionRef.current = "";
    if (sessionId) void api.deleteChatSession(sessionId).catch(() => {});
    setGenerating(false);
    onBusyChange(false);
  };

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="workflow-builder-runtime">
          {t(($) => $.page.create.ai.runtime_label)}
        </Label>
        {runtimesLoading ? (
          <div className="flex h-9 items-center gap-2 text-body text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {t(($) => $.page.create.ai.loading_runtimes)}
          </div>
        ) : codexRuntimes.length > 0 ? (
          <select
            id="workflow-builder-runtime"
            value={runtimeId}
            disabled={generating || creating}
            onChange={(event) => {
              setRuntimeId(event.target.value);
              setDraft(null);
              setError("");
            }}
            className="h-9 w-full rounded-md border border-input bg-transparent px-3 text-body outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {codexRuntimes.map((runtime) => (
              <option key={runtime.id} value={runtime.id}>
                {runtime.custom_name || runtime.name}
              </option>
            ))}
          </select>
        ) : (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-body text-destructive"
          >
            <AlertCircle className="mt-0.5 size-4 shrink-0" />
            <span>{t(($) => $.page.create.ai.no_codex_runtime)}</span>
          </div>
        )}
      </div>

      <div className="space-y-1.5">
        <Label htmlFor="workflow-builder-description">
          {t(($) => $.page.create.ai.request_label)}
        </Label>
        <Textarea
          id="workflow-builder-description"
          autoFocus
          rows={6}
          value={request}
          disabled={generating || creating}
          className="resize-none"
          placeholder={t(($) => $.page.create.ai.request_placeholder)}
          onChange={(event) => {
            setRequest(event.target.value);
            setDraft(null);
            setError("");
          }}
        />
        <p className="text-caption text-muted-foreground">
          {t(($) => $.page.create.ai.request_hint)}
        </p>
      </div>

      {generating ? (
        <div className="flex items-center gap-3 rounded-md border bg-muted/30 px-3 py-3">
          <Loader2 className="size-4 shrink-0 animate-spin text-primary" />
          <div>
            <p className="text-body font-medium">
              {t(($) => $.page.create.ai.generating)}
            </p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.page.create.ai.generating_hint)}
            </p>
          </div>
        </div>
      ) : null}

      {draft ? (
        <div className="space-y-3 rounded-lg border bg-muted/20 p-4">
          <div className="flex items-center gap-2">
            <Sparkles className="size-4 text-primary" />
            <p className="font-medium">
              {t(($) => $.page.create.ai.preview_title)}
            </p>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="generated-workflow-name">
                {t(($) => $.page.create.name_label)}
              </Label>
              <Input
                id="generated-workflow-name"
                maxLength={200}
                value={draft.name}
                disabled={creating}
                onChange={(event) =>
                  setDraft({ ...draft, name: event.target.value })
                }
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="generated-workflow-key">
                {t(($) => $.page.create.key_label)}
              </Label>
              <Input
                id="generated-workflow-key"
                maxLength={128}
                value={draft.key}
                disabled={creating}
                spellCheck={false}
                aria-invalid={!draftKeyValid}
                className="font-mono"
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    key: event.target.value.toLowerCase(),
                  })
                }
              />
            </div>
          </div>
          {draft.description ? (
            <p className="text-body text-muted-foreground">
              {draft.description}
            </p>
          ) : null}
          <ol className="space-y-1.5">
            {draft.definition.nodes.map((node, index) => (
              <li
                key={node.key}
                className="flex items-center gap-2 text-body"
              >
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-caption text-primary">
                  {index + 1}
                </span>
                <span className="min-w-0 flex-1 truncate">
                  {node.name || node.key}
                </span>
                <span className="rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                  {node.type}
                </span>
              </li>
            ))}
          </ol>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.page.create.ai.review_hint)}
          </p>
        </div>
      ) : null}

      {error ? (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md bg-destructive/10 px-3 py-2 text-body text-destructive"
        >
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <span className="whitespace-pre-wrap">{error}</span>
        </div>
      ) : null}

      <div className="flex items-center justify-end gap-2 pt-1">
        {generating ? (
          <Button type="button" variant="outline" onClick={stopGeneration}>
            {t(($) => $.page.create.ai.stop)}
          </Button>
        ) : (
          <Button
            type="button"
            variant="outline"
            disabled={creating}
            onClick={onCancel}
          >
            {t(($) => $.page.create.cancel)}
          </Button>
        )}
        {draft && !generating ? (
          <>
            <Button
              type="button"
              variant="outline"
              disabled={creating}
              onClick={() => void generate()}
            >
              {t(($) => $.page.create.ai.regenerate)}
            </Button>
            <Button
              type="button"
              disabled={
                creating || !draft.name.trim() || !draftKeyValid
              }
              onClick={() =>
                void onCreate({
                  key: draft.key.trim(),
                  name: draft.name.trim(),
                  description: draft.description,
                  definition: draft.definition,
                })
              }
            >
              {creating ? (
                <Loader2 className="size-4 animate-spin" />
              ) : (
                <Sparkles className="size-4" />
              )}
              {creating
                ? t(($) => $.page.create.creating)
                : t(($) => $.page.create.ai.create_generated)}
            </Button>
          </>
        ) : (
          <Button
            type="button"
            disabled={
              !request.trim() ||
              !runtimeId ||
              generating ||
              creating ||
              codexRuntimes.length === 0
            }
            onClick={() => void generate()}
          >
            {generating ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Sparkles className="size-4" />
            )}
            {generating
              ? t(($) => $.page.create.ai.generating_button)
              : t(($) => $.page.create.ai.generate)}
          </Button>
        )}
      </div>
    </div>
  );
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
}
