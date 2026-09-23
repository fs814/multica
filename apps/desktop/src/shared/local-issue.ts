export interface LocalIssue {
  id: string;
  title: string;
  description?: string;
  machine: "local";
  directory: string;
  provider: string;
  status: string;
  output?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

export interface CreateLocalIssueRequest {
  title: string;
  description: string;
  machine?: "local";
  directory: string;
  provider: string;
}

/** Non-secret user-level capability inventory from the local daemon. */
export interface LocalCapabilities {
  provider: string;
  skills: { key: string; name: string; root?: string }[];
  skills_supported: boolean;
  mcp_servers: { name: string; transport?: string; enabled: boolean }[];
  mcp_supported: boolean;
}
