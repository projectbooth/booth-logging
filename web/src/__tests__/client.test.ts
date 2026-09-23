import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, fetchConfig, fetchLogs, fetchModules } from "../api/client";
import { fail, mockFetch, ok } from "./testUtils";

afterEach(() => vi.unstubAllGlobals());

const ctx = (token: string | null = "tok", workspace = "acme") => ({ workspace, getAccessToken: () => token });

describe("request plumbing (ADR 0025 / 0033)", () => {
  it("goes through booth-core's gateway prefix with the workspace header and a fresh token", async () => {
    const m = mockFetch([["GET /config", () => ok({})]]);
    let token = "old";
    const c = { workspace: "globex", getAccessToken: () => token };
    await fetchConfig(c);
    token = "refreshed";
    await fetchConfig(c);
    expect(m.requests[0].url).toBe("/modules/logging/api/config");
    expect(m.requests.map((r) => r.headers.get("Authorization"))).toEqual(["Bearer old", "Bearer refreshed"]);
    expect(m.requests[0].headers.get("X-Workspace")).toBe("globex");
  });

  it("omits Authorization for a null token", async () => {
    const m = mockFetch([["GET /modules", () => ok({ modules: [] })]]);
    await fetchModules(ctx(null));
    expect(m.requests[0].headers.has("Authorization")).toBe(false);
  });
});

describe("fetchLogs", () => {
  it("encodes repeated module/level params, search, range and limit", async () => {
    const m = mockFetch([[/^GET \/logs\?/, () => ok({ entries: [], query: "" })]]);
    await fetchLogs(ctx(), { modules: ["storage", "catalog"], levels: ["error", "warn"], search: 'a "quoted" & odd', start: "2026-09-22T11:00:00.000Z", end: "1790078400000000000", limit: 50 });
    const q = new URL(m.requests[0].url, "http://x").searchParams;
    expect(q.getAll("module")).toEqual(["storage", "catalog"]);
    expect(q.getAll("level")).toEqual(["error", "warn"]);
    expect(q.get("q")).toBe('a "quoted" & odd');
    expect(q.get("start")).toBe("2026-09-22T11:00:00.000Z");
    expect(q.get("end")).toBe("1790078400000000000");
    expect(q.get("limit")).toBe("50");
  });

  it("leaves out an empty search", async () => {
    const m = mockFetch([[/^GET \/logs\?/, () => ok({ entries: [], query: "" })]]);
    await fetchLogs(ctx(), { modules: [], levels: [], search: "", start: "s", end: "e" });
    expect(new URL(m.requests[0].url, "http://x").searchParams.has("q")).toBe(false);
  });

  it("surfaces the server's error message and field", async () => {
    mockFetch([[/^GET \/logs\?/, () => fail(400, "time range is longer than the maximum of 336h0m0s", "start")]]);
    const err = await fetchLogs(ctx(), { modules: [], levels: [], search: "", start: "s", end: "e" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(400);
    expect((err as ApiError).field).toBe("start");
  });

  it("falls back to the raw body for a non-JSON error", async () => {
    mockFetch([["GET /modules", () => ({ status: 502, text: "Bad Gateway" })]]);
    await expect(fetchModules(ctx())).rejects.toThrow("Bad Gateway");
  });
});
