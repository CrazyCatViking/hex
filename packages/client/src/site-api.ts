export interface SiteActionDefinition {
  name: string;
  description: string;
  version: number;
  exported: boolean;
  readOnly?: boolean;
  inputSchema: Record<string, unknown>;
  outputSchema: Record<string, unknown>;
}

export interface SiteInvocation {
  id: string;
  source: string;
  target: string;
  action: string;
  version: number;
  actor: string;
  status: "running" | "succeeded" | "uncertain";
  startedAt: string;
  output?: unknown;
}
