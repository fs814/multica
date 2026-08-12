"use client";

import { use } from "react";
import { WorkflowTemplateDetailPage } from "@multica/views/workflows/components";

export default function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <WorkflowTemplateDetailPage templateId={id} />;
}
