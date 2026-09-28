import { useState } from "react";
import type { LogEntry } from "../types";
import { LevelBadge } from "./ui";

/** Formats an RFC 3339 timestamp in the viewer's local time, to the millisecond. */
export function formatTimestamp(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  const pad = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

/** Pretty-prints a JSON log line; returns null for anything that isn't a JSON object. */
export function prettyJson(line: string): string | null {
  const t = line.trim();
  if (!t.startsWith("{")) return null;
  try {
    return JSON.stringify(JSON.parse(t), null, 2);
  } catch {
    return null;
  }
}

/** Newest-first log lines. A row expands to show where the line came from and, for a JSON
 *  line (what ADR 0022 recommends modules write), the line pretty-printed. */
export function LogTable({ entries }: { entries: LogEntry[] }) {
  const [open, setOpen] = useState<string | null>(null);

  return (
    <ol className="divide-y divide-slate-100 overflow-hidden rounded-lg border border-slate-200 font-mono text-xs dark:divide-slate-800 dark:border-slate-700">
      {entries.map((e, i) => {
        // cursor alone isn't unique: two lines can share a nanosecond across pods.
        const key = `${e.cursor}-${i}`;
        const expanded = open === key;
        const pretty = expanded ? prettyJson(e.line) : null;
        return (
          <li key={key} className="bg-white dark:bg-slate-950">
            <button
              type="button"
              aria-expanded={expanded}
              onClick={() => setOpen(expanded ? null : key)}
              className="flex w-full items-start gap-2 px-2 py-1 text-left hover:bg-slate-50 dark:hover:bg-slate-900"
            >
              <time dateTime={e.timestamp} title={e.timestamp} className="shrink-0 text-slate-500 dark:text-slate-400">
                {formatTimestamp(e.timestamp)}
              </time>
              <LevelBadge level={e.level} />
              <span className="w-28 shrink-0 truncate text-indigo-700 dark:text-indigo-300" title={e.module}>
                {e.module}
              </span>
              <span className={`min-w-0 flex-1 ${expanded ? "whitespace-pre-wrap break-all" : "truncate"}`}>{e.line}</span>
            </button>
            {expanded && (
              <div className="border-t border-slate-100 bg-slate-50 px-3 py-2 dark:border-slate-800 dark:bg-slate-900">
                <dl className="grid grid-cols-[max-content_1fr] gap-x-3 gap-y-0.5 font-sans">
                  {(
                    [
                      ["Time (UTC)", e.timestamp],
                      ["Module", e.module],
                      ["Namespace", e.namespace],
                      ["Pod", e.pod],
                      ["Container", e.container],
                      ["Stream", e.stream],
                      ["Workspace", e.workspace],
                    ] as const
                  )
                    .filter(([, v]) => v)
                    .map(([k, v]) => (
                      <div key={k} className="contents">
                        <dt className="text-slate-500 dark:text-slate-400">{k}</dt>
                        <dd className="font-mono">{v}</dd>
                      </div>
                    ))}
                </dl>
                {pretty && <pre className="mt-2 overflow-x-auto whitespace-pre text-slate-800 dark:text-slate-200">{pretty}</pre>}
              </div>
            )}
          </li>
        );
      })}
    </ol>
  );
}
