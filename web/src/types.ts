// Mirrors internal/api/server.go's JSON shapes — hand-written rather than generated, the
// same trade-off the sibling modules made. Keep the two in sync by hand.

/** Caller's role in the active workspace (ADR 0025), per NativeModuleProps (ADR 0031). */
export type WorkspaceRole = "owner" | "editor" | "viewer";

/** Severity buckets the API filters on. Several of Loki's detected levels collapse into
 *  one bucket (error = error|critical|fatal, debug = debug|trace). */
export type Level = "error" | "warn" | "info" | "debug" | "unknown";

export const LEVELS: Level[] = ["error", "warn", "info", "debug", "unknown"];

export interface LoggingConfig {
  retentionSeconds: number;
  maxQueryRangeSeconds: number;
  maxLimit: number;
  levels: Level[];
}

export interface LogEntry {
  /** RFC 3339 with nanoseconds, UTC. */
  timestamp: string;
  /** Unix nanoseconds as a string; pass as `end` to continue strictly before this line. */
  cursor: string;
  line: string;
  level: Level;
  module: string;
  namespace?: string;
  pod?: string;
  container?: string;
  stream?: string;
}

export interface LogsResponse {
  entries: LogEntry[];
  nextCursor?: string;
  /** The LogQL that ran. */
  query: string;
}

export interface LogQuery {
  modules: string[];
  levels: Level[];
  search: string;
  /** RFC 3339 or Unix nanoseconds. */
  start: string;
  /** RFC 3339 or Unix nanoseconds (exclusive). */
  end: string;
  limit?: number;
}
