import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LoggingApp } from "../LoggingApp";
import type { WorkspaceRole } from "../types";
import { config, entry, fail, mockFetch, ok, type Routes } from "./testUtils";

afterEach(() => vi.unstubAllGlobals());

const getAccessToken = () => "tok";
const renderApp = (role: WorkspaceRole = "owner") =>
  render(<LoggingApp workspace="acme" role={role} theme="light" getAccessToken={getAccessToken} />);

const baseRoutes = (logs: Routes[number][1], cfg: Record<string, unknown> = config): Routes => [
  ["GET /config", () => ok(cfg)],
  ["GET /modules", () => ok({ modules: ["catalog", "storage"] })],
  [/^GET \/logs\?/, logs],
];

const params = (url: string) => new URL(url, "http://x").searchParams;

describe("LoggingApp", () => {
  it("doesn't call the API for a non-owner and explains why", () => {
    const m = mockFetch([]);
    renderApp("editor");
    expect(screen.getByRole("status")).toHaveTextContent(/Only workspace owners can read logs/);
    expect(m.fn).not.toHaveBeenCalled();
  });

  it("loads the last hour of every module by default and shows the lines", async () => {
    const m = mockFetch(baseRoutes(() => ok({ entries: [entry(), entry({ cursor: "1", line: "plain", level: "unknown", module: "catalog" })], query: '{module=~".+"}' })));
    renderApp();
    expect(await screen.findByText('{"level":"error","msg":"db down"}')).toBeInTheDocument();
    expect(screen.getByText("plain")).toBeInTheDocument();

    const q = params(m.calls("GET", /\/logs\?/)[0].url);
    expect(q.getAll("module")).toEqual([]);
    expect(q.getAll("level")).toEqual([]);
    expect(Date.parse(q.get("end")!) - Date.parse(q.get("start")!)).toBe(3600_000);
    expect(q.get("limit")).toBe("200");
  });

  it("filters by module chips, severity chips, search and range", async () => {
    const user = userEvent.setup();
    const m = mockFetch(baseRoutes(() => ok({ entries: [], query: "" })));
    renderApp();
    await screen.findByText(/No log lines match/);

    await user.click(within(screen.getByRole("group", { name: "Modules" })).getByRole("button", { name: "storage" }));
    await user.click(within(screen.getByRole("group", { name: "Severity" })).getByRole("button", { name: "Error" }));
    await user.selectOptions(screen.getByLabelText("Time range"), "Last 24 hours");
    await user.type(screen.getByLabelText("Search log lines"), "  timeout  {enter}");

    await waitFor(() => {
      const q = params(m.calls("GET", /\/logs\?/).at(-1)!.url);
      expect(q.get("q")).toBe("timeout");
    });
    const q = params(m.calls("GET", /\/logs\?/).at(-1)!.url);
    expect(q.getAll("module")).toEqual(["storage"]);
    expect(q.getAll("level")).toEqual(["error"]);
    expect(Date.parse(q.get("end")!) - Date.parse(q.get("start")!)).toBe(86400_000);
    expect(within(screen.getByRole("group", { name: "Modules" })).getByRole("button", { name: "storage" })).toHaveAttribute("aria-pressed", "true");
  });

  it("hides ranges longer than the server allows", async () => {
    mockFetch(baseRoutes(() => ok({ entries: [], query: "" }), { ...config, maxQueryRangeSeconds: 7 * 86400 }));
    renderApp();
    await screen.findByText(/No log lines match/);
    await waitFor(() => expect(screen.queryByRole("option", { name: "Last 14 days" })).not.toBeInTheDocument());
    expect(screen.getByRole("option", { name: "Last 7 days" })).toBeInTheDocument();
  });

  it("pages older lines with the cursor, keeping the same start", async () => {
    const user = userEvent.setup();
    const m = mockFetch(
      baseRoutes((req) =>
        params(req.url).get("end") === "100"
          ? ok({ entries: [entry({ cursor: "50", line: "older line" })], query: "" })
          : ok({ entries: [entry({ cursor: "100", line: "newer line" })], nextCursor: "100", query: "" }),
      ),
    );
    renderApp();
    await screen.findByText("newer line");
    await user.click(screen.getByRole("button", { name: "Load older" }));
    await screen.findByText("older line");
    expect(screen.getByText("newer line")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load older" })).not.toBeInTheDocument();

    const [first, second] = m.calls("GET", /\/logs\?/).map((r) => params(r.url));
    expect(second.get("start")).toBe(first.get("start"));
    expect(second.get("end")).toBe("100");
  });

  it("shows the server's refusal (e.g. an owner outside the designated workspaces)", async () => {
    mockFetch([
      ["GET /config", () => fail(403, "this deployment only lets owners of designated workspaces read logs")],
      ["GET /modules", () => fail(403, "this deployment only lets owners of designated workspaces read logs")],
      [/^GET \/logs\?/, () => fail(403, "this deployment only lets owners of designated workspaces read logs")],
    ]);
    renderApp();
    expect(await screen.findByRole("alert")).toHaveTextContent("designated workspaces");
  });

  it("expands a JSON line into its source and pretty-printed body", async () => {
    const user = userEvent.setup();
    mockFetch(baseRoutes(() => ok({ entries: [entry()], query: "" })));
    renderApp();
    await user.click(await screen.findByRole("button", { expanded: false }));
    expect(screen.getByText("booth-storage-abc")).toBeInTheDocument();
    expect(screen.getByText("stdout")).toBeInTheDocument();
    expect(screen.getByText(/"msg": "db down"/)).toBeInTheDocument();
  });

  it("tells a workspace-scoped owner they see only their workspace's own pods (ADR 0077)", async () => {
    mockFetch(baseRoutes(() => ok({ entries: [], query: "" }), { ...config, scope: "workspace", workspace: "acme" }));
    renderApp();
    const banner = await screen.findByText(/own pods only/);
    expect(banner).toHaveTextContent("acme");
    expect(banner).toHaveTextContent(/shared platform services aren't included/);
  });

  it("shows no scope notice to an operator", async () => {
    mockFetch(baseRoutes(() => ok({ entries: [], query: "" })));
    renderApp();
    await screen.findByText(/No log lines match/);
    expect(screen.queryByText(/own pods only/)).not.toBeInTheDocument();
  });

  it("drops a stale response that lands after a newer filter's", async () => {
    const user = userEvent.setup();
    let releaseFirst: () => void = () => {};
    const firstDone = new Promise<void>((r) => (releaseFirst = r));
    let n = 0;
    mockFetch(
      baseRoutes(async () => {
        if (++n === 1) {
          await firstDone;
          return ok({ entries: [entry({ line: "STALE" })], query: "" });
        }
        return ok({ entries: [entry({ line: "fresh" })], query: "" });
      }),
    );
    renderApp();
    await screen.findByRole("button", { name: "Error" });
    await user.click(screen.getByRole("button", { name: "Error" }));
    await screen.findByText("fresh");
    releaseFirst();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText("STALE")).not.toBeInTheDocument();
  });
});
