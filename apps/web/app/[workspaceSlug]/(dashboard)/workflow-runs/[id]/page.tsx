"use client";

import { use } from "react";
import { WorkflowRunDetailPage } from "@multica/views/workflows/components";

export default function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <WorkflowRunDetailPage runId={id} />;
}
