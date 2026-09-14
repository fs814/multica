"use client";
import { use } from "react";
import { WorkflowDebugDetailPage } from "@multica/views/workflows/components";
export default function Page({params}: {params: Promise<{id: string}>}) {
 const {id} = use(params); return <WorkflowDebugDetailPage key={id} runId={id}/>;
}
