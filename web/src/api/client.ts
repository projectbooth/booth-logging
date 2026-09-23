import type { LogQuery, LoggingConfig, LogsResponse } from "../types";

// Mounted inside booth-design's shell, so requests resolve against the shell's origin and
// must go through booth-core's gateway at /modules/{id}/* — which strips /modules/logging
// before forwarding to this repo's own /api/... routes. A bare "/api/..." would hit
// booth-core's own API instead (booth-module-store's first-release bug). The dev harness's
// Vite proxy mimics the same prefix-stripping.
const BASE = "/modules/logging/api";

export type GetAccessToken = () => string | null;

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public field?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Everything a request needs from the mounting shell (ADR 0031/0033). */
export interface ApiContext {
  workspace: string;
  getAccessToken: GetAccessToken;
}

// X-Workspace on every request (ADR 0025). The token is read fresh each time, never
// cached (ADR 0033), and a null token omits the header rather than sending "Bearer null".
function buildHeaders(ctx: ApiContext): Headers {
  const headers = new Headers();
  headers.set("X-Workspace", ctx.workspace);
  const token = ctx.getAccessToken();
  if (token !== null) headers.set("Authorization", `Bearer ${token}`);
  return headers;
}

async function toApiError(res: Response): Promise<ApiError> {
  const text = await res.text();
  try {
    const body = JSON.parse(text) as { error?: string; field?: string };
    if (body.error) return new ApiError(res.status, body.error, body.field);
  } catch {
    // not JSON — e.g. a gateway error page
  }
  return new ApiError(res.status, text || res.statusText || `HTTP ${res.status}`);
}

async function get<T>(ctx: ApiContext, path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(BASE + path, { headers: buildHeaders(ctx), signal });
  if (!res.ok) throw await toApiError(res);
  return (await res.json()) as T;
}

export const fetchConfig = (ctx: ApiContext) => get<LoggingConfig>(ctx, "/config");

export const fetchModules = (ctx: ApiContext) =>
  get<{ modules: string[] }>(ctx, "/modules").then((r) => r.modules);

export function fetchLogs(ctx: ApiContext, q: LogQuery, signal?: AbortSignal): Promise<LogsResponse> {
  const p = new URLSearchParams();
  for (const m of q.modules) p.append("module", m);
  for (const l of q.levels) p.append("level", l);
  if (q.search) p.set("q", q.search);
  p.set("start", q.start);
  p.set("end", q.end);
  if (q.limit) p.set("limit", String(q.limit));
  return get<LogsResponse>(ctx, `/logs?${p.toString()}`, signal);
}
