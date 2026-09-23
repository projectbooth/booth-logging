import type { ButtonHTMLAttributes, ReactNode } from "react";
import type { Level } from "../types";

// Small presentational primitives in the same Tailwind slate/indigo palette as
// booth-storage and booth-module-store, so native modules look alike in the shell. Every
// colour has a dark: counterpart — booth-design toggles dark mode via data-theme.

type Variant = "primary" | "secondary";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-indigo-600 text-white hover:bg-indigo-500 disabled:opacity-50",
  secondary:
    "border border-slate-300 text-slate-700 hover:bg-slate-100 disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800",
};

export function Button({
  variant = "secondary",
  className = "",
  type = "button",
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant }) {
  return (
    <button
      type={type}
      className={`rounded-md px-3 py-1.5 text-sm font-medium focus:outline-none focus:ring-2 focus:ring-indigo-500 ${VARIANTS[variant]} ${className}`}
      {...rest}
    />
  );
}

export const inputClass =
  "rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-900 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-100";

export function Banner({ tone, children }: { tone: "error" | "info"; children: ReactNode }) {
  const tones = {
    error: "border-red-200 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200",
    info: "border-slate-200 bg-slate-50 text-slate-700 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300",
  };
  return (
    <div role={tone === "error" ? "alert" : "status"} className={`rounded-md border px-3 py-2 text-sm ${tones[tone]}`}>
      {children}
    </div>
  );
}

/** A toggle chip for multi-select filters; aria-pressed carries the state. */
export function Chip({ pressed, onClick, children, className = "" }: { pressed: boolean; onClick: () => void; children: ReactNode; className?: string }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={`rounded-full border px-2.5 py-0.5 text-xs font-medium focus:outline-none focus:ring-2 focus:ring-indigo-500 ${
        pressed
          ? "border-indigo-500 bg-indigo-50 text-indigo-800 dark:border-indigo-400 dark:bg-indigo-950 dark:text-indigo-200"
          : "border-slate-300 text-slate-600 hover:bg-slate-100 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
      } ${className}`}
    >
      {children}
    </button>
  );
}

export const LEVEL_LABELS: Record<Level, string> = {
  error: "Error",
  warn: "Warn",
  info: "Info",
  debug: "Debug",
  unknown: "Unknown",
};

const LEVEL_TONES: Record<Level, string> = {
  error: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
  warn: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  info: "bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300",
  debug: "bg-slate-200 text-slate-700 dark:bg-slate-800 dark:text-slate-300",
  unknown: "bg-slate-100 text-slate-500 dark:bg-slate-900 dark:text-slate-400",
};

export function LevelBadge({ level }: { level: Level }) {
  return (
    <span className={`inline-block w-16 rounded px-1.5 py-0.5 text-center text-xs font-medium ${LEVEL_TONES[level] ?? LEVEL_TONES.unknown}`}>
      {LEVEL_LABELS[level] ?? level}
    </span>
  );
}
