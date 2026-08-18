"use client";

import { useCallback, useEffect } from "react";
import { AlertCircle, ListTodo, Workflow } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { workflowTemplateListOptions } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import type {
  Issue,
  IssueTableFacetSpec,
  IssueTableFacetsResponse,
  WorkingAgentSummary,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { PageHeader } from "../../layout/page-header";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { IssueSurface } from "../surface/issue-surface";
import { IssuesHeader } from "./issues-header";

function WorkflowIssuesSurfaceHeader({
  issues,
  workingAgents,
  isRefreshing,
  facetCountsExact,
  tableFacetCounts,
  onTableFacetChange,
}: {
  issues: Issue[];
  workingAgents: WorkingAgentSummary[] | undefined;
  isRefreshing: boolean;
  facetCountsExact: boolean;
  tableFacetCounts?: IssueTableFacetsResponse;
  onTableFacetChange: (facet: IssueTableFacetSpec | null) => void;
}) {
  const dateFilter = useViewStore((state) => state.dateFilter);
  const setDateFilter = useViewStore((state) => state.setDateFilter);

  return (
    <IssuesHeader
      scopedIssues={issues}
      workingAgents={workingAgents}
      dateFilter={dateFilter}
      onDateFilterChange={setDateFilter}
      isRefreshing={isRefreshing}
      facetCountsExact={facetCountsExact}
      tableFacetCounts={tableFacetCounts}
      onTableFacetChange={onTableFacetChange}
      showScopeTabs={false}
    />
  );
}

export function WorkflowIssuesPage() {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const navigation = useNavigation();
  const workflowIssuesPath = wsPaths.workflowIssues();
  const requestedTemplateId = navigation.searchParams.get("template") ?? "";
  const {
    data: templates = [],
    isLoading,
    error,
    refetch,
  } = useQuery(workflowTemplateListOptions(wsId));
  const selectedTemplate =
    templates.find((template) => template.id === requestedTemplateId) ??
    templates[0] ??
    null;
  const selectedTemplateId = selectedTemplate?.id ?? "";

  const selectTemplate = useCallback(
    (templateId: string) => {
      navigation.replace(
        `${workflowIssuesPath}?template=${encodeURIComponent(templateId)}`,
      );
    },
    [navigation, workflowIssuesPath],
  );

  useEffect(() => {
    if (
      navigation.pathname === workflowIssuesPath &&
      selectedTemplateId &&
      selectedTemplateId !== requestedTemplateId
    ) {
      selectTemplate(selectedTemplateId);
    }
  }, [
    navigation.pathname,
    requestedTemplateId,
    selectTemplate,
    selectedTemplateId,
    workflowIssuesPath,
  ]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader className="gap-2">
        <ListTodo className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-body font-medium">
          {t(($) => $.issues.title)}
        </h1>
        <div className="ml-auto flex items-center gap-2">
          <span className="hidden text-caption text-muted-foreground sm:inline">
            {t(($) => $.issues.select_label)}
          </span>
          <Select<string>
            items={templates.map((template) => ({
              value: template.id,
              label: template.name,
            }))}
            value={selectedTemplate?.id ?? ""}
            onValueChange={(value) => {
              if (value === null) return;
              selectTemplate(value);
            }}
            disabled={isLoading || templates.length === 0}
          >
            <SelectTrigger
              className="h-8 w-52 max-w-[45vw]"
              aria-label={t(($) => $.issues.select_label)}
            >
              <SelectValue placeholder={t(($) => $.issues.select_placeholder)} />
            </SelectTrigger>
            <SelectContent>
              {templates.map((template) => (
                <SelectItem key={template.id} value={template.id}>
                  {template.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </PageHeader>

      {error ? (
        <div
          role="alert"
          className="flex flex-1 flex-col items-center justify-center gap-3 text-muted-foreground"
        >
          <AlertCircle className="h-10 w-10 text-destructive" />
          <p className="text-body">{t(($) => $.issues.error_title)}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>
            {t(($) => $.issues.retry)}
          </Button>
        </div>
      ) : isLoading ? (
        <div className="flex flex-1 items-center justify-center text-body text-muted-foreground">
          {t(($) => $.issues.loading)}
        </div>
      ) : !selectedTemplate ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2 text-muted-foreground">
          <Workflow className="h-10 w-10 text-faint-foreground" />
          <p className="text-body">{t(($) => $.issues.no_workflows_title)}</p>
          <p className="text-caption">{t(($) => $.issues.no_workflows_hint)}</p>
        </div>
      ) : (
        <IssueSurface
          scope={{ type: "workflow", templateId: selectedTemplate.id }}
          surfaceKey="workflow-issues"
          modes={["board", "list", "table", "swimlane"]}
          batchToolbar="list"
          renderHeader={({ controller }) => (
            <WorkflowIssuesSurfaceHeader
              issues={controller.surfaceIssues}
              workingAgents={controller.workingAgents}
              isRefreshing={controller.isRefreshing}
              facetCountsExact={controller.facetCountsExact}
              tableFacetCounts={controller.tableFacetCounts}
              onTableFacetChange={controller.setActiveTableFacet}
            />
          )}
          renderEmpty={() => (
            <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 text-muted-foreground">
              <ListTodo className="h-10 w-10 text-faint-foreground" />
              <p className="text-body">{t(($) => $.issues.empty_title)}</p>
              <p className="text-caption">{t(($) => $.issues.empty_hint)}</p>
            </div>
          )}
        />
      )}
    </div>
  );
}
