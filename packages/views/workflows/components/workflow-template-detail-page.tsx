"use client";

/**
 * The workflow graph editor page.
 *
 * This file is only composition and policy: the graph translation lives in
 * ../graph, the canvas in ../canvas, and every control in ../editor. What it owns
 * is the set of decisions that cannot be made by any of those in isolation.
 *
 * ## Server state vs editor state
 *
 * React Query owns the fetched template; `useReducer` owns the working graph,
 * the selection and the undo stack (plan section 4). They meet in exactly one
 * place - a `hydrate` dispatch keyed on the template id - because React Query
 * refetches on window focus, on reconnect and after every mutation settles, and
 * a naive "seed state from data" effect would discard the author's unsaved work
 * on each of those. See editor-state.ts's `hydrate` case for the full argument
 * and for what happens when someone else saves the template while it is open.
 *
 * ## Why read-only is computed from three independent facts
 *
 * They are different refusals. A built-in template is refused by the server for
 * everyone (`PATCH` -> 409, "built-in workflow templates cannot be edited"); an
 * archived one is refused because archival is one-way, so a draft made here could
 * never be published; an ordinary member is refused because the route requires
 * owner|admin. All three must disable saving, but the reasons are not
 * interchangeable and the UI says which one applies - a greyed Save with no
 * explanation reads as a bug.
 *
 * ## Why publishing while dirty needs a confirmation
 *
 * Publish freezes an *immutable* version that every future Run pins. Publishing
 * with unsaved edits freezes the last *saved* graph, so the author would ship a
 * version they can see is not what is on their screen, and could never edit it
 * afterwards. That is unrecoverable data loss dressed up as a successful action,
 * so it gets a dialog offering to save first rather than a toast afterwards.
 */

import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import {
  ArrowLeft,
  CircleAlert,
  CircleCheck,
  Copy,
  Loader2,
  Play,
  Upload,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { memberListOptions } from "@multica/core/workspace/queries";
import {
  usePublishWorkflowTemplate,
  useDuplicateWorkflowTemplate,
  useUpdateWorkflowTemplate,
  workflowTemplateDetailOptions,
} from "@multica/core/workflows";
import type { WorkflowDefinition } from "@multica/core/workflows";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { WorkflowCanvas } from "../canvas/workflow-canvas";
import {
  clientValidateGraph,
  editorEdgeId,
  type FlowEdge,
  type WorkflowNodeType,
} from "../graph";
import { AddNodeToolbar } from "../editor/add-node-toolbar";
import {
  canRedo,
  canUndo,
  initialWorkflowEditorState,
  isDirty,
  selectedWorkflowNode,
  workflowEditorReducer,
  workingDefinition,
} from "../editor/editor-state";
import { WorkflowEditorToolbar } from "../editor/editor-toolbar";
import { WorkflowJsonView } from "../editor/json-view";
import { WorkflowPropertiesPanel } from "../editor/properties-panel";
import { WorkflowRunDialog } from "../runs/components/workflow-run-dialog";
import { WorkflowStatusBadge } from "./workflow-status-badge";

/**
 * What the problems strip is currently showing.
 *
 * `source` exists because the three producers answer different questions and an
 * author must be able to tell them apart: the client mirror says "this build
 * believes the server will reject this", the server says so authoritatively, and
 * a save rejection says "the graph you just tried to write was refused". Folding
 * them into one list would leave "no problems found" ambiguous about whether
 * anything was actually checked.
 */
type ProblemReport = {
  source: "client" | "server" | "save";
  messages: string[];
};

export function WorkflowTemplateDetailPage({
  templateId,
}: {
  templateId: string;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const navigation = useNavigation();

  const { data, isLoading, error, refetch } = useQuery(
    workflowTemplateDetailOptions(wsId, templateId),
  );

  const [state, dispatch] = useReducer(
    workflowEditorReducer,
    undefined,
    initialWorkflowEditorState,
  );
  const [problems, setProblems] = useState<ProblemReport | null>(null);
  const [validating, setValidating] = useState(false);
  const [publishPrompt, setPublishPrompt] = useState(false);
  const [runOpen, setRunOpen] = useState(false);
  const [saveConflict, setSaveConflict] = useState<WorkflowDefinition | null>(
    null,
  );

  const saveTemplate = useUpdateWorkflowTemplate();
  const publishTemplate = usePublishWorkflowTemplate();
  const duplicateTemplate = useDuplicateWorkflowTemplate();

  // The one join between server state and editor state. `hydrate` is idempotent
  // per template id, so this effect firing again on a background refetch is a
  // no-op rather than a silent revert - the guard lives in the reducer so it
  // cannot be defeated by a change to this dependency array.
  const definition = data?.definition;
  const readable = data?.key !== undefined && data.key !== "";
  useEffect(() => {
    if (!readable || !definition) return;
    dispatch({ type: "hydrate", templateId, definition });
  }, [readable, definition, templateId]);

  const working = useMemo(() => workingDefinition(state), [state]);
  const dirty = useMemo(() => isDirty(state), [state]);
  const selected = useMemo(() => selectedWorkflowNode(state), [state]);

  // Editor state is a plain reducer with no persistence, so a closed tab or
  // window takes the unsaved graph with it and there is nothing to recover from.
  // The page already treats losing unsaved work as serious enough to warrant a
  // dialog before publishing; a tab close loses strictly more, so it gets the
  // browser's own confirmation. In-app navigation is not covered here — the
  // router would have to own that — but this catches the destructive cases the
  // page can actually intercept (tab close, reload, desktop window close).
  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      // Assigning returnValue is what actually triggers the prompt in Chromium;
      // the string itself is ignored by every current browser.
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  // Role gate, reusing the pattern the runtimes and squads pages already use
  // (find self in the cached member list, compare role) rather than introducing a
  // new permissions mechanism. `memberListOptions` is the same cache those pages
  // read, so the surfaces cannot disagree about who is an admin.
  const currentUserId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const myRole = currentUserId
    ? (members.find((member) => member.user_id === currentUserId)?.role ?? null)
    : null;
  const isAdmin = myRole === "owner" || myRole === "admin";

  const builtin = data?.is_builtin === true;
  const archived = data?.status === "archived";
  // While the member list is in flight `myRole` is null, which reads as "not an
  // admin". That is the correct direction to fail: a Save button that appears and
  // then disables would invite a click the server answers with 403, whereas one
  // that appears a moment late costs nothing.
  const readOnly = builtin || archived || !isAdmin;

  const readOnlyReason = builtin
    ? t(($) => $.editor.read_only.builtin)
    : archived
      ? t(($) => $.editor.read_only.archived)
      : t(($) => $.editor.read_only.role);

  // ---- actions ------------------------------------------------------------

  const handleValidate = useCallback(async () => {
    // The client mirror runs first and unconditionally: it is synchronous, so the
    // author gets an answer on the same frame they asked, and if it finds a
    // problem the server round trip would only restate it.
    const local = clientValidateGraph(working);
    if (local.length > 0) {
      setProblems({ source: "client", messages: local });
      return;
    }
    setValidating(true);
    try {
      const result = await api.validateWorkflowDefinition(working);
      setProblems({ source: "server", messages: result.messages });
    } catch (err) {
      // This endpoint answers "your graph is wrong" with a 200, so a throw here
      // is a transport failure, not a verdict. Reporting it as a validation
      // problem would tell the author their graph is broken when it may be fine.
      setProblems({
        source: "server",
        messages: [errorMessage(err, t(($) => $.editor.validate_failed))],
      });
    } finally {
      setValidating(false);
    }
  }, [working, t]);

  const handleSave = useCallback(async () => {
    const sent = working;
    try {
      await saveTemplate.mutateAsync({
        id: templateId,
        definition: sent,
        revision: data?.revision ?? 0,
      });
      // `sent`, not the response's `definition`: a save over a published
      // template returns the *published* bytes because the new draft is
      // deliberately invisible to Runs until publish (see api/client.ts). Using
      // the response would make the author's own edit appear to vanish.
      dispatch({ type: "mark_saved", definition: sent });
      setProblems(null);
      toast.success(t(($) => $.editor.toast_saved));
    } catch (err) {
      if (isWorkflowTemplateRevisionConflict(err)) {
        // Keep the exact bytes the user attempted to save. Background refetches
        // are intentionally ignored by the reducer, so nothing can overwrite
        // this working copy while the author chooses copy, reload, or retry.
        setSaveConflict(sent);
        return;
      }
      // A 422 carries the server's per-rule messages. Surfacing them inline is
      // the whole point: "save failed" in a toast tells an author nothing they
      // can act on, while `Agent node "implement" must have exactly one outgoing
      // edge, got 2` names both the node and the rule.
      const messages = validationMessages(err);
      if (messages.length > 0) {
        setProblems({ source: "save", messages });
        return;
      }
      toast.error(errorMessage(err, t(($) => $.editor.toast_save_failed)));
    }
  }, [data?.revision, saveTemplate, templateId, working, t]);

  const copyConflictedJSON = useCallback(async () => {
    if (!saveConflict) return;
    try {
      await navigator.clipboard.writeText(
        JSON.stringify(saveConflict, null, 2),
      );
      toast.success(t(($) => $.editor.conflict.copied));
    } catch {
      toast.error(t(($) => $.editor.conflict.copy_failed));
    }
  }, [saveConflict, t]);

  const reloadAfterConflict = useCallback(async () => {
    const latest = (await refetch()).data;
    if (!latest) {
      toast.error(t(($) => $.editor.conflict.reload_failed));
      return;
    }
    dispatch({
      type: "reload_from_server",
      templateId,
      definition: latest.definition,
    });
    setSaveConflict(null);
    setProblems(null);
  }, [refetch, t, templateId]);

  const retryAfterConflict = useCallback(async () => {
    if (!saveConflict) return;
    const latest = (await refetch()).data;
    if (!latest) {
      toast.error(t(($) => $.editor.conflict.reload_failed));
      return;
    }
    try {
      await saveTemplate.mutateAsync({
        id: templateId,
        definition: saveConflict,
        revision: latest.revision,
      });
      dispatch({ type: "mark_saved", definition: saveConflict });
      setSaveConflict(null);
      setProblems(null);
      toast.success(t(($) => $.editor.toast_saved));
    } catch (err) {
      if (!isWorkflowTemplateRevisionConflict(err)) {
        toast.error(
          errorMessage(
            err,
            t(($) => $.editor.conflict.retry_failed),
          ),
        );
      }
    }
  }, [refetch, saveConflict, saveTemplate, t, templateId]);

  const runPublish = useCallback(async () => {
    try {
      await publishTemplate.mutateAsync(templateId);
      toast.success(t(($) => $.detail.toast_published));
    } catch (err) {
      const messages = validationMessages(err);
      if (messages.length > 0) {
        setProblems({ source: "save", messages });
        return;
      }
      toast.error(errorMessage(err, t(($) => $.detail.toast_publish_failed)));
    }
  }, [publishTemplate, templateId, t]);

  /** Save, then publish - the "publish what I can see" answer to the dialog. */
  const saveThenPublish = useCallback(async () => {
    const sent = working;
    try {
      await saveTemplate.mutateAsync({
        id: templateId,
        definition: sent,
        revision: data?.revision ?? 0,
      });
      dispatch({ type: "mark_saved", definition: sent });
    } catch (err) {
      const messages = validationMessages(err);
      setProblems({
        source: "save",
        messages:
          messages.length > 0
            ? messages
            : [errorMessage(err, t(($) => $.editor.toast_save_failed))],
      });
      // Deliberately does NOT fall through to publish. Publishing after a failed
      // save would freeze the older graph - exactly the outcome the dialog exists
      // to prevent.
      return;
    }
    await runPublish();
  }, [data?.revision, saveTemplate, templateId, working, runPublish, t]);

  const handlePublish = useCallback(() => {
    if (dirty) {
      setPublishPrompt(true);
      return;
    }
    void runPublish();
  }, [dirty, runPublish]);

  const handleDuplicate = useCallback(async () => {
    if (!data?.id) return;
    try {
      const copied = await duplicateTemplate.mutateAsync(data.id);
      if (!copied.id || !copied.key) {
        toast.error(t(($) => $.detail.toast_duplicate_unreadable));
        return;
      }
      toast.success(t(($) => $.detail.toast_duplicated));
      navigation.push(wsPaths.workflowDetail(copied.id));
    } catch (err) {
      toast.error(errorMessage(err, t(($) => $.detail.toast_duplicate_failed)));
    }
  }, [data?.id, duplicateTemplate, navigation, t, wsPaths]);

  const handleAddNode = useCallback((nodeType: WorkflowNodeType) => {
    dispatch({ type: "add_node", nodeType });
  }, []);

  const handleApplyJson = useCallback((next: WorkflowDefinition) => {
    dispatch({ type: "apply_definition", definition: next });
    // The pasted graph is a different graph; a problem list describing the old
    // one would be read as describing this one.
    setProblems(null);
  }, []);

  const handleConnect = useCallback(
    (source: string, target: string) => {
      // A dragged connection is a `next` edge. The alternatives would both be
      // guesses the author did not make: `rework` would invent a bounded cycle,
      // and `branch` would invent a verdict. The properties panel is where a
      // verdict or a rework target is declared, and the canvas re-derives those
      // edges from the node once it is.
      //
      // The id embeds the source's current out-degree so two edges out of the
      // same node cannot collide - xyflow drops duplicate ids, which would hide
      // the second edge from the author *and* from the validator's complaint.
      const index = state.present.edges.filter(
        (edge) => edge.source === source,
      ).length;
      const edge: FlowEdge = {
        id: editorEdgeId("next", source, index, target),
        source,
        target,
        type: "next",
        data: { kind: "next", sourceKey: source, targetKey: target },
      };
      dispatch({
        type: "set_graph",
        nodes: state.present.nodes,
        edges: [...state.present.edges, edge],
      });
    },
    [state.present.edges, state.present.nodes],
  );

  // ---- render -------------------------------------------------------------

  if (isLoading) return <EditorSkeleton />;

  // A schema miss does not throw: parseWithFallback returns the empty fallback,
  // and because the client spreads the requested `id` onto it, `data` and
  // `data.id` are always truthy. `key` is the field that still distinguishes the
  // two - the server always sends a non-empty key, the fallback leaves it "" - so
  // it is the gate. Without it an unreadable response would render a blank
  // template whose status defaults to "draft", offering a Publish button that
  // freezes a version the user was never shown.
  if (error || !data || !data.key) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground">
        {t(($) => $.detail.not_found)}
      </div>
    );
  }

  // Publishability is a property of the VERSION list, not of the template's
  // status. `workflow_template.status` goes draft -> published and never back
  // (no query in workflow.sql returns it), while PATCH deliberately opens a NEW
  // draft version when every existing version is published — that is how a
  // published template is meant to evolve. Gating on `status === "draft"` would
  // therefore strand exactly that work: the edit saves, and the button that
  // could ship it never appears again.
  const draftVersion = (data.versions ?? []).find(
    (version) => version.status === "draft",
  );
  const canPublish = draftVersion !== undefined && !builtin && isAdmin;

  // Runnability is a property of the VERSION list too, and for the mirror-image
  // reason publishability is: a Run pins a *published* version, so what matters
  // is whether one exists — not `status`, which a template reaches once and
  // never leaves, and not `current_version`, which the summary schema defaults
  // to null when it cannot be read (that default would hide a runnable
  // template's Run button on a transient contract drift, whereas an absent
  // published version is a real, permanent refusal).
  //
  // Archived is a separate refusal from unpublished even though both disable the
  // button. An archived template may well HAVE a published version; it is
  // refused because archival is one-way, so a run started now would execute a
  // graph the workspace has retired. The two get different explanations because
  // the fixes differ: publish, versus nothing (duplicate it instead).
  const hasPublishedVersion = (data.versions ?? []).some(
    (version) => version.status === "published",
  );
  const runnable = hasPublishedVersion && !archived;
  const runRefusal: "unpublished" | "archived" | undefined = archived
    ? "archived"
    : hasPublishedVersion
      ? undefined
      : "unpublished";
  const runRefusalReason =
    runRefusal === "archived"
      ? t(($) => $.runs.dialog.archived)
      : runRefusal === "unpublished"
        ? t(($) => $.runs.dialog.unpublished)
        : undefined;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 items-start gap-3 border-b px-4 py-2.5">
        <Button
          variant="ghost"
          size="icon-sm"
          className="mt-0.5 shrink-0"
          aria-label={t(($) => $.editor.back)}
          render={<AppLink href={wsPaths.workflows()} />}
        >
          <ArrowLeft className="size-4" aria-hidden="true" />
        </Button>

        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <h1 className="min-w-0 truncate text-body font-medium">{data.name}</h1>
            <WorkflowStatusBadge status={data.status} />
            {builtin ? (
              <Badge variant="secondary">
                {t(($) => $.detail.builtin_badge)}
              </Badge>
            ) : null}
          </div>
          {data.description ? (
            <p className="mt-0.5 truncate text-caption text-muted-foreground">
              {data.description}
            </p>
          ) : null}
          <p className="mt-0.5 truncate font-mono text-micro text-muted-foreground">
            {t(($) => $.editor.key_line, { key: data.key })}
          </p>
        </div>

        <div className="flex shrink-0 items-center gap-1">
          <WorkflowEditorToolbar
            dirty={dirty}
            saving={saveTemplate.isPending}
            validating={validating}
            readOnly={readOnly}
            onValidate={() => void handleValidate()}
            onAutoLayout={() => dispatch({ type: "auto_layout" })}
            onToggleJson={() => dispatch({ type: "toggle_json" })}
            onSave={() => void handleSave()}
            onUndo={() => dispatch({ type: "undo" })}
            onRedo={() => dispatch({ type: "redo" })}
            canUndo={canUndo(state)}
            canRedo={canRedo(state)}
          />
          {builtin ? (
            <Button
              size="sm"
              variant="outline"
              disabled={!isAdmin || duplicateTemplate.isPending}
              title={!isAdmin ? t(($) => $.editor.read_only.role) : undefined}
              onClick={() => void handleDuplicate()}
              className="px-2 sm:px-2.5"
              aria-label={t(($) => $.detail.duplicate)}
            >
              {duplicateTemplate.isPending ? (
                <Loader2
                  className="size-3.5 animate-spin motion-reduce:animate-none sm:mr-1"
                  aria-hidden="true"
                />
              ) : (
                <Copy className="size-3.5 sm:mr-1" aria-hidden="true" />
              )}
              <span className="hidden sm:inline">
                {duplicateTemplate.isPending
                  ? t(($) => $.detail.duplicating)
                  : t(($) => $.detail.duplicate)}
              </span>
            </Button>
          ) : null}
          {/* Run sits next to Publish and is always rendered, disabled when the
              template cannot start one. Withholding it would leave a reader who
              came here to run something with no evidence the action exists;
              disabling it with the reason stated in the row below says both that
              it exists and what would make it available. */}
          <Button
            size="sm"
            variant="outline"
            disabled={!runnable}
            title={runnable ? undefined : runRefusalReason}
            onClick={() => setRunOpen(true)}
            className="px-2 sm:px-2.5"
            aria-label={t(($) => $.runs.dialog.trigger)}
          >
            <Play className="size-3.5 sm:mr-1" aria-hidden="true" />
            <span className="hidden sm:inline">
              {t(($) => $.runs.dialog.trigger)}
            </span>
          </Button>
          {canPublish ? (
            <Button
              size="sm"
              onClick={handlePublish}
              disabled={publishTemplate.isPending}
              className="px-2 sm:px-2.5"
              aria-label={t(($) => $.detail.publish)}
            >
              {publishTemplate.isPending ? (
                <Loader2
                  className="size-3.5 animate-spin motion-reduce:animate-none sm:mr-1"
                  aria-hidden="true"
                />
              ) : (
                <Upload className="size-3.5 sm:mr-1" aria-hidden="true" />
              )}
              <span className="hidden sm:inline">
                {publishTemplate.isPending
                  ? t(($) => $.detail.publishing)
                  : t(($) => $.detail.publish)}
              </span>
            </Button>
          ) : null}
        </div>
      </header>

      <div className="flex shrink-0 items-center gap-3 border-b px-4 py-2">
        {/* The working graph, not the fetched one: the toolbar disables its
            "+ Input" pill when an input node already exists, and the node the
            author added a moment ago is only in the working copy until a save
            lands. Reading the server's definition would let a second one through. */}
        <AddNodeToolbar
          readOnly={readOnly}
          definition={working}
          onAdd={handleAddNode}
        />
        {readOnly ? (
          <span className="truncate text-caption text-muted-foreground">
            {readOnlyReason}
          </span>
        ) : null}
        {/* Shown even when a read-only reason is already present: they answer
            different questions ("can I edit this" vs "can I run this"), and a
            built-in template is the common case where the answers differ - it is
            uneditable by everyone and perfectly runnable. */}
        {runRefusalReason ? (
          <span className="truncate text-caption text-muted-foreground">
            {runRefusalReason}
          </span>
        ) : null}
      </div>

      {problems ? (
        <ProblemsStrip report={problems} onDismiss={() => setProblems(null)} />
      ) : null}

      <div className="flex min-h-0 flex-1">
        {state.jsonOpen ? (
          // The JSON view replaces the canvas rather than sitting beside it: both
          // are full editors for the same graph, and two live editors for one
          // document would need a conflict story that the "stale draft" notice
          // inside the JSON view already declines to invent.
          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto p-4">
            <WorkflowJsonView
              definition={working}
              readOnly={readOnly}
              onApply={handleApplyJson}
            />
          </div>
        ) : (
          <WorkflowCanvas
            nodes={state.present.nodes}
            edges={state.present.edges}
            selectedNodeId={state.selectedNodeId}
            readOnly={readOnly}
            onNodesChange={(nodes) =>
              dispatch({ type: "set_graph", nodes, edges: state.present.edges })
            }
            onEdgesChange={(edges) =>
              dispatch({ type: "set_graph", nodes: state.present.nodes, edges })
            }
            onSelectNode={(nodeId) => dispatch({ type: "select", nodeId })}
            onConnect={handleConnect}
          />
        )}

        <WorkflowPropertiesPanel
          node={selected}
          definition={working}
          readOnly={readOnly}
          onChange={(node) => dispatch({ type: "patch_node", node })}
        />
      </div>

      <WorkflowRunDialog
        templateId={templateId}
        templateName={data.name}
        // The FETCHED definition, not the editor's working copy. A run pins the
        // published version, so the intake form must be read off the graph a run
        // would actually pin - collecting fields from an unsaved draft would show a
        // form whose values the pinned graph never declared and whose required ones
        // the engine would not check.
        definition={data.definition}
        runnable={runnable}
        refusal={runRefusal}
        open={runOpen}
        onOpenChange={setRunOpen}
      />

      <AlertDialog open={publishPrompt} onOpenChange={setPublishPrompt}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.editor.publish_dirty.title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.editor.publish_dirty.body)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.editor.publish_dirty.cancel)}
            </AlertDialogCancel>
            {/* Publishing the saved graph stays available - an author may
                genuinely want to ship the last saved version and keep editing -
                but it is the secondary action, because it is the one that freezes
                something other than what is on screen. */}
            <AlertDialogAction
              variant="outline"
              onClick={() => {
                setPublishPrompt(false);
                void runPublish();
              }}
            >
              {t(($) => $.editor.publish_dirty.publish_saved)}
            </AlertDialogAction>
            <AlertDialogAction
              onClick={() => {
                setPublishPrompt(false);
                void saveThenPublish();
              }}
            >
              {t(($) => $.editor.publish_dirty.save_and_publish)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={saveConflict !== null}
        onOpenChange={(open) => {
          if (!open) setSaveConflict(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.editor.conflict.title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.editor.conflict.body)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.editor.conflict.keep_editing)}
            </AlertDialogCancel>
            <Button
              type="button"
              variant="outline"
              onClick={() => void copyConflictedJSON()}
            >
              {t(($) => $.editor.conflict.copy_json)}
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => void reloadAfterConflict()}
            >
              {t(($) => $.editor.conflict.reload)}
            </Button>
            <Button type="button" onClick={() => void retryAfterConflict()}>
              {t(($) => $.editor.conflict.retry)}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Problems
// ---------------------------------------------------------------------------

/**
 * The validation report.
 *
 * Messages are rendered verbatim, untranslated. That is deliberate and matches
 * the graph model's `clientValidateGraph`: these are the server's own strings,
 * the client mirror emits the identical text, and translating one side would make
 * an author fixing one problem see two different descriptions of it depending on
 * which side happened to report it first. The chrome around them - heading,
 * count, dismiss - is translated as usual.
 */
function ProblemsStrip({
  report,
  onDismiss,
}: {
  report: ProblemReport;
  onDismiss(): void;
}) {
  const { t } = useT("workflows");
  const clean = report.messages.length === 0;

  return (
    <div
      className={
        clean
          ? "flex shrink-0 items-start gap-2 border-b border-emerald-500/30 bg-emerald-500/5 px-4 py-2"
          : "flex shrink-0 items-start gap-2 border-b border-destructive/30 bg-destructive/5 px-4 py-2"
      }
    >
      {clean ? (
        <CircleCheck
          className="mt-0.5 size-3.5 shrink-0 text-emerald-600 dark:text-emerald-500"
          aria-hidden="true"
        />
      ) : (
        <CircleAlert
          className="mt-0.5 size-3.5 shrink-0 text-destructive"
          aria-hidden="true"
        />
      )}
      <div className="min-w-0 flex-1">
        <p className="text-caption font-medium">
          {clean
            ? t(($) => $.editor.problems.none)
            : report.source === "save"
              ? t(($) => $.editor.problems.rejected)
              : t(($) => $.editor.problems.found, {
                  count: report.messages.length,
                })}
        </p>
        {clean ? null : (
          <ul className="mt-1 flex flex-col gap-0.5">
            {report.messages.map((message, index) => (
              // Index key: the messages are a positional list of strings with no
              // identity of their own, and duplicates are legitimate (two nodes
              // can break the same rule with the same wording).
              <li
                key={index}
                className="font-mono text-micro leading-snug break-words text-muted-foreground"
              >
                {message}
              </li>
            ))}
          </ul>
        )}
      </div>
      <Button
        variant="ghost"
        size="sm"
        className="shrink-0"
        onClick={onDismiss}
        aria-label={t(($) => $.editor.problems.dismiss)}
      >
        {t(($) => $.editor.problems.dismiss)}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Error shapes
// ---------------------------------------------------------------------------

/**
 * The 422 body's `messages` array, or `[]`.
 *
 * Read off `ApiError.body` rather than by pattern-matching `err.message`: the
 * handler answers a rejected graph with
 * `{"error":"invalid workflow definition","messages":[...]}`, and the array is
 * the only part an author can act on. Structural, so a reworded `error` string on
 * the server cannot silently turn an inline problem list back into a toast.
 */
function validationMessages(err: unknown): string[] {
  if (!err || typeof err !== "object") return [];
  const body = (err as { body?: unknown }).body;
  if (!body || typeof body !== "object") return [];
  const messages = (body as { messages?: unknown }).messages;
  if (!Array.isArray(messages)) return [];
  return messages.filter(
    (message): message is string => typeof message === "string",
  );
}

function isWorkflowTemplateRevisionConflict(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const candidate = err as { status?: unknown; body?: unknown };
  if (
    candidate.status !== 409 ||
    !candidate.body ||
    typeof candidate.body !== "object"
  ) {
    return false;
  }
  return (
    (candidate.body as { code?: unknown }).code ===
    "workflow_template_revision_conflict"
  );
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

// ---------------------------------------------------------------------------
// Skeleton
// ---------------------------------------------------------------------------

/**
 * Mirrors the editor's frame - header, toolbar row, canvas, panel - rather than a
 * generic block. The canvas is the part that takes longest to become
 * interactive, so a skeleton that already shows where the panel will be avoids a
 * layout jump at the moment the author starts aiming at controls.
 */
function EditorSkeleton() {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-3 border-b px-4 py-2.5">
        <Skeleton className="size-7 rounded-md" />
        <div className="flex flex-1 flex-col gap-1.5">
          <Skeleton className="h-4 w-48" />
          <Skeleton className="h-3 w-32" />
        </div>
        <Skeleton className="h-8 w-56" />
      </div>
      <div className="flex shrink-0 gap-1.5 border-b px-4 py-2">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-7 w-20 rounded-full" />
        ))}
      </div>
      <div className="flex min-h-0 flex-1">
        <div className="flex-1 p-6">
          <Skeleton className="h-full w-full rounded-lg" />
        </div>
        <div className="w-80 shrink-0 border-l p-4">
          <Skeleton className="h-full w-full rounded-lg" />
        </div>
      </div>
    </div>
  );
}
