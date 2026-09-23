// Public entry point for @projectbooth/logging-ui (ADR 0030). Everything booth-design needs
// is re-exported here; anything else under src/ is internal and may change freely.
//
// Consumers must also import the stylesheet once: `@projectbooth/logging-ui/dist/style.css`.
import "./library.css";

export { LoggingApp } from "./LoggingApp";
export type { LoggingAppProps } from "./LoggingApp";
export type { WorkspaceRole, Level, LogEntry } from "./types";
