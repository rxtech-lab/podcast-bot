import "server-only";
import type { DiscussionScript, VideoConfig } from "@/lib/schema/script-types";

// engine.ts is the server-side REST client for the debate-bot Go engine. It
// attaches the shared service token (never exposed to the browser) so every
// call is authenticated. Browser-facing streams (SSE/WS) go through the
// app/api/engine/* proxy routes instead.

const BASE = process.env.ENGINE_BASE_URL ?? "http://localhost:8080";
const TOKEN = process.env.DASHBOARD_SERVICE_TOKEN ?? "";

export async function engineFetch(
  path: string,
  init: RequestInit = {},
): Promise<Response> {
  const headers = new Headers(init.headers);
  if (TOKEN) headers.set("Authorization", `Bearer ${TOKEN}`);
  return fetch(`${BASE}${path}`, { ...init, headers, cache: "no-store" });
}

async function engineJSON<T>(path: string, init: RequestInit = {}): Promise<T> {
  const resp = await engineFetch(path, init);
  if (!resp.ok) {
    const body = await resp.text().catch(() => "");
    throw new Error(`engine ${path} → ${resp.status}: ${body.slice(0, 300)}`);
  }
  return (await resp.json()) as T;
}

// ---- Models ----------------------------------------------------------------

export interface ModelInfo {
  id: string;
  label: string;
  provider: string;
  capabilities?: string[];
  default_for?: string[];
}
export interface ModelsResponse {
  defaults: { host: string; scene_planner: string; compression: string };
  models: ModelInfo[];
}

export function getModels(): Promise<ModelsResponse> {
  return engineJSON<ModelsResponse>("/api/models");
}

// ---- Tools -----------------------------------------------------------------

export interface ToolMeta {
  name: string;
  description: string;
  schema?: Record<string, unknown>;
  source: string;
  content_types?: string[];
  roles?: string[];
  dynamic?: boolean;
}

export function getTools(): Promise<{ tools: ToolMeta[] }> {
  return engineJSON<{ tools: ToolMeta[] }>("/api/tools");
}

// ---- Planning --------------------------------------------------------------

export interface PlanResponse {
  script: DiscussionScript;
  markdown: string;
  researched: boolean;
}

export function planScript(req: {
  type: string;
  topic: string;
  language?: string;
  channel?: string;
  discussants?: number;
  research?: boolean;
}): Promise<PlanResponse> {
  return engineJSON<PlanResponse>("/api/plan", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
}

export function improveScript(req: {
  previousScript: DiscussionScript;
  instruction: string;
}): Promise<PlanResponse> {
  return engineJSON<PlanResponse>("/api/plan/improve", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
}

// ---- Jobs ------------------------------------------------------------------

export function submitJobJSON(req: {
  script: DiscussionScript;
  videoConfig: VideoConfig;
}): Promise<{ id: string }> {
  return engineJSON<{ id: string }>("/api/jobs/json", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
}

export interface EngineJob {
  id: string;
  status: "pending" | "running" | "done" | "error";
  title?: string;
  type?: string;
  error?: string;
  has_video?: boolean;
  download_url?: string;
  phase?: string;
  phase_label?: string;
  elapsed_ms?: number;
  remaining_ms?: number;
}

export function getJob(id: string): Promise<EngineJob> {
  return engineJSON<EngineJob>(`/api/jobs/${encodeURIComponent(id)}`);
}

export async function sendParticipantMessage(
  jobId: string,
  text: string,
): Promise<void> {
  // Video-mode participation endpoint — injects into the running job's
  // orchestrator. (Stream-mode's /api/messages is not mounted in video mode.)
  const resp = await engineFetch(
    `/api/jobs/${encodeURIComponent(jobId)}/messages`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
    },
  );
  if (!resp.ok && resp.status !== 204) {
    throw new Error(`send message failed: ${resp.status}`);
  }
}
