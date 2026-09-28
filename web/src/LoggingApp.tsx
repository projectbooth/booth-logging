import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ApiContext, GetAccessToken } from "./api/client";
import { fetchConfig, fetchLogs, fetchModules } from "./api/client";
import { Banner, Button, Chip, inputClass, LEVEL_LABELS } from "./components/ui";
import { LogTable } from "./components/LogTable";
import { errorMessage, useLoad } from "./hooks";
import { LEVELS, type Level, type LogEntry, type WorkspaceRole } from "./types";

/**
 * Props contract pinned by ADR 0031 (workspace/role/theme) and ADR 0033 (getAccessToken):
 * plain React props, so this package never imports anything from booth-design.
 */
export interface LoggingAppProps {
  workspace: string;
  /** UX gating only — the API enforces the owner-only rule itself on every request. */
  role: WorkspaceRole;
  theme: "dark" | "light";
  getAccessToken: GetAccessToken;
}

/** Time-range presets, in seconds. Presets longer than the server's cap are hidden. */
export const RANGES: Array<{ label: string; seconds: number }> = [
  { label: "Last 15 minutes", seconds: 15 * 60 },
  { label: "Last hour", seconds: 3600 },
  { label: "Last 6 hours", seconds: 6 * 3600 },
  { label: "Last 24 hours", seconds: 24 * 3600 },
  { label: "Last 3 days", seconds: 3 * 86400 },
  { label: "Last 7 days", seconds: 7 * 86400 },
  { label: "Last 14 days", seconds: 14 * 86400 },
  { label: "Last 30 days", seconds: 30 * 86400 },
];

const PAGE_SIZE = 200;

interface Applied {
  modules: string[];
  levels: Level[];
  search: string;
  rangeSeconds: number;
}

type Results =
  | { status: "loading" }
  | { status: "error"; error: string }
  | { status: "ready"; entries: LogEntry[]; nextCursor?: string; query: string; start: string; loadingMore: boolean; moreError?: string };

/**
 * booth-logging's native viewer (ADR 0015): browse, filter and search every module's logs
 * by module, time range and severity. Lines are whatever modules wrote to stdout/stderr —
 * collected by the node-level collector with no per-module integration (ADR 0022).
 */
export function LoggingApp({ workspace, role, theme, getAccessToken }: LoggingAppProps) {
  const ctx = useMemo<ApiContext>(() => ({ workspace, getAccessToken }), [workspace, getAccessToken]);

  return (
    <div data-theme={theme} className="flex flex-col gap-4 p-6 text-slate-900 dark:text-slate-100">
      <div>
        <h2 className="text-lg font-semibold">Logs</h2>
        <p className="mt-0.5 text-sm text-slate-500 dark:text-slate-400">
          What modules and pods write to stdout and stderr.
        </p>
      </div>
      {role === "owner" ? (
        <Viewer ctx={ctx} />
      ) : (
        <Banner tone="info">
          Only workspace owners can read logs. Ask an owner if you need something from them.
        </Banner>
      )}
    </div>
  );
}

function Viewer({ ctx }: { ctx: ApiContext }) {
  const config = useLoad(() => fetchConfig(ctx), [ctx]);
  const modules = useLoad(() => fetchModules(ctx), [ctx]);

  const [applied, setApplied] = useState<Applied>({ modules: [], levels: [], search: "", rangeSeconds: 3600 });
  const [searchInput, setSearchInput] = useState("");
  const [results, setResults] = useState<Results>({ status: "loading" });
  const generation = useRef(0);

  const maxRange = config.state.status === "ready" ? config.state.data.maxQueryRangeSeconds : Infinity;
  const ranges = RANGES.filter((r) => r.seconds <= maxRange);

  // Every run starts a new generation; a response from an older one is dropped, so a slow
  // query can never overwrite the results of a newer filter.
  const run = useCallback(() => {
    const mine = ++generation.current;
    const end = new Date();
    const start = new Date(end.getTime() - applied.rangeSeconds * 1000).toISOString();
    setResults({ status: "loading" });
    fetchLogs(ctx, { modules: applied.modules, levels: applied.levels, search: applied.search, start, end: end.toISOString(), limit: PAGE_SIZE }).then(
      (r) => {
        if (mine === generation.current) setResults({ status: "ready", entries: r.entries, nextCursor: r.nextCursor, query: r.query, start, loadingMore: false });
      },
      (err: unknown) => {
        if (mine === generation.current) setResults({ status: "error", error: errorMessage(err) });
      },
    );
  }, [ctx, applied]);

  useEffect(run, [run]);

  const loadOlder = () => {
    if (results.status !== "ready" || !results.nextCursor) return;
    const mine = generation.current;
    const current = results;
    setResults({ ...current, loadingMore: true, moreError: undefined });
    fetchLogs(ctx, { modules: applied.modules, levels: applied.levels, search: applied.search, start: current.start, end: current.nextCursor!, limit: PAGE_SIZE }).then(
      (r) => {
        if (mine === generation.current)
          setResults({ ...current, entries: [...current.entries, ...r.entries], nextCursor: r.nextCursor, loadingMore: false });
      },
      (err: unknown) => {
        if (mine === generation.current) setResults({ ...current, loadingMore: false, moreError: errorMessage(err) });
      },
    );
  };

  const toggle = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

  return (
    <>
      {config.state.status === "ready" && config.state.data.scope === "workspace" && (
        <Banner tone="info">
          You're seeing logs from workspace <strong>{config.state.data.workspace}</strong>'s own pods only, such as its
          notebook servers. Logs from shared platform services aren't included; your deployment's operators can read those.
        </Banner>
      )}
      <form
        className="flex flex-col gap-3 rounded-lg border border-slate-200 p-3 dark:border-slate-700"
        onSubmit={(e) => {
          e.preventDefault();
          setApplied((a) => ({ ...a, search: searchInput.trim() }));
        }}
      >
        <div className="flex flex-wrap items-center gap-2">
          <input
            type="search"
            aria-label="Search log lines"
            placeholder="Search log lines (case-insensitive)"
            value={searchInput}
            maxLength={500}
            onChange={(e) => setSearchInput(e.target.value)}
            className={`${inputClass} min-w-0 flex-1`}
          />
          <select
            aria-label="Time range"
            value={applied.rangeSeconds}
            onChange={(e) => setApplied((a) => ({ ...a, rangeSeconds: Number(e.target.value) }))}
            className={inputClass}
          >
            {ranges.map((r) => (
              <option key={r.seconds} value={r.seconds}>
                {r.label}
              </option>
            ))}
          </select>
          <Button type="submit" variant="primary">
            Search
          </Button>
          <Button onClick={run}>Refresh</Button>
        </div>

        <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Severity">
          <span className="mr-1 text-xs font-medium text-slate-500 dark:text-slate-400">Severity</span>
          {LEVELS.map((l) => (
            <Chip key={l} pressed={applied.levels.includes(l)} onClick={() => setApplied((a) => ({ ...a, levels: toggle(a.levels, l) }))}>
              {LEVEL_LABELS[l]}
            </Chip>
          ))}
        </div>

        <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Modules">
          <span className="mr-1 text-xs font-medium text-slate-500 dark:text-slate-400">Modules</span>
          {modules.state.status === "loading" && <span className="text-xs text-slate-400">Loading…</span>}
          {modules.state.status === "error" && <span className="text-xs text-red-600 dark:text-red-400">Couldn't load modules: {modules.state.error}</span>}
          {modules.state.status === "ready" &&
            (modules.state.data.length === 0 ? (
              <span className="text-xs text-slate-400">No logs collected yet.</span>
            ) : (
              modules.state.data.map((m) => (
                <Chip key={m} pressed={applied.modules.includes(m)} onClick={() => setApplied((a) => ({ ...a, modules: toggle(a.modules, m) }))}>
                  {m}
                </Chip>
              ))
            ))}
        </div>
        {(applied.levels.length === 0 || applied.modules.length === 0) && (
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {applied.modules.length === 0 && "All modules. "}
            {applied.levels.length === 0 && "All severities. "}
            Select chips to narrow.
          </p>
        )}
      </form>

      {results.status === "loading" && <p className="text-sm text-slate-500 dark:text-slate-400">Loading logs…</p>}
      {results.status === "error" && <Banner tone="error">{results.error}</Banner>}
      {results.status === "ready" && (
        <>
          {results.entries.length === 0 ? (
            <Banner tone="info">No log lines match these filters in this time range.</Banner>
          ) : (
            <LogTable entries={results.entries} />
          )}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <details className="text-xs text-slate-500 dark:text-slate-400">
              <summary className="cursor-pointer">LogQL</summary>
              <code className="mt-1 block break-all font-mono">{results.query}</code>
            </details>
            {results.nextCursor && (
              <Button onClick={loadOlder} disabled={results.loadingMore}>
                {results.loadingMore ? "Loading…" : "Load older"}
              </Button>
            )}
          </div>
          {results.moreError && <Banner tone="error">{results.moreError}</Banner>}
        </>
      )}
    </>
  );
}
