"use client";
import { use } from "react";
import { WorkflowInstanceDetailPage } from "@multica/views/workflows/components";
export default function Page({params}:{params:Promise<{id:string}>}){const {id}=use(params);return <WorkflowInstanceDetailPage instanceId={id}/>;}
