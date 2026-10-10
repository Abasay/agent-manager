/**
 * Copyright (c) 2025, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { ThemeProvider, createTheme } from "@wso2/oxygen-ui";
import type {
  TraceExportResponse,
  TraceFilters,
  TraceOverview,
} from "@agent-management-platform/types";

// api-client crashes at import time outside a configured app shell, and
// EnvironmentSelector pulls it in; stub both at the module boundary.
vi.mock("@agent-management-platform/api-client", () => ({
  useTraceList: vi.fn(),
  useExportTraces: vi.fn(() => ({ mutateAsync: vi.fn(), isPending: false })),
  useGetOrganization: vi.fn(() => ({
    data: { namespace: "ns" },
    isPending: false,
    isSuccess: true,
  })),
  useGetAgent: vi.fn(() => ({ data: { uuid: "agent-uuid" }, isPending: false, isSuccess: true })),
  useListEnvironments: vi.fn(() => ({
    data: [{ name: "dev" }],
    isPending: false,
    isSuccess: true,
  })),
  isObserverConfigured: vi.fn(() => true),
  useTrack: vi.fn(() => ({ track: vi.fn() })),
  ConsoleAction: { TraceOpened: "trace_opened", DownloadArtifact: "download_artifact" },
  useTrace: vi.fn(() => ({ data: undefined, isLoading: true, isTruncated: false })),
  useTraceScores: vi.fn(() => ({ data: undefined, isLoading: true })),
  useSpanDetail: vi.fn(() => ({ data: undefined, isLoading: false })),
}));
vi.mock("@agent-management-platform/shared-component", () => ({
  EnvironmentSelector: () => null,
  copyToClipboard: vi.fn(() => Promise.resolve(true)),
}));

import { useExportTraces, useTrace, useTraceList } from "@agent-management-platform/api-client";
import { copyToClipboard } from "@agent-management-platform/shared-component";
import { TracesComponent } from "./Traces.Component";
import { parseTraceFilters, traceFilterChips } from "./traceFilters";
import { parseTraceColumns } from "./traceColumns";
import { formatStartTime } from "./traceTime";

const mockUseTraceList = vi.mocked(useTraceList);

const makeTrace = (
  traceId: string,
  errorCount = 0,
  extra: Partial<TraceOverview> = {},
): TraceOverview => ({
  traceId,
  rootSpanId: `${traceId}-root`,
  rootSpanName: `root ${traceId}`,
  rootSpanKind: "agent",
  startTime: "2026-10-01T10:00:00Z",
  endTime: "2026-10-01T10:00:05Z",
  durationInNanos: 5_000_000_000,
  spanCount: 12,
  status: { errorCount },
  ...extra,
} as TraceOverview);

// The filtered list keeps only t-err; the unfiltered list holds both.
const ALL = [
  makeTrace("t-err", 1, { conversationId: "conv-1", models: ["gpt-4o"] }),
  makeTrace("t-ok", 0),
];
const listCache = new Map<string, unknown>();
/** Cached list data for the filters; status=error keeps only errored traces. */
const listFor = (filters: TraceFilters = {}) => {
  const key = JSON.stringify(filters);
  if (!listCache.has(key)) {
    const traces = filters.status === "error" ? ALL.filter((t) => t.status?.errorCount) : ALL;
    listCache.set(key, {
      traces,
      totalCount: traces.length,
      fetchedRange: { startTime: "2026-10-01T09:00:00Z", endTime: "2026-10-01T11:00:00Z" },
    });
  }
  return listCache.get(key);
};

const lastFilters = () => mockUseTraceList.mock.lastCall?.[9]?.filters;
const lastIncludeModels = () => mockUseTraceList.mock.lastCall?.[9]?.includeModels;

// Hook fields a test can override, such as truncated or hasMore.
let hookOverrides: Record<string, unknown> = {};
const loadMore = vi.fn();

// jsdom has no IntersectionObserver; tests capture each observer and trigger it by hand.
const observers: { callback: IntersectionObserverCallback; targets: Element[]; live: boolean }[] =
  [];
class FakeIntersectionObserver {
  private record: (typeof observers)[number];
  constructor(callback: IntersectionObserverCallback) {
    this.record = { callback, targets: [], live: true };
    observers.push(this.record);
  }
  observe(target: Element) {
    this.record.targets.push(target);
  }
  unobserve() {}
  disconnect() {
    this.record.live = false;
  }
  takeRecords() {
    return [];
  }
}
vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);

// Reports every observed sentinel as in view.
const scrollToSentinel = () =>
  act(() => {
    for (const o of observers.filter((x) => x.live)) {
      const entries = o.targets.map((target) => ({ isIntersecting: true, target }));
      o.callback(entries as unknown as IntersectionObserverEntry[], {} as IntersectionObserver);
    }
  });

beforeEach(() => {
  vi.clearAllMocks();
  listCache.clear();
  observers.length = 0;
  hookOverrides = {};
  mockUseTraceList.mockImplementation((...args) => ({
    data: listFor(args[9]?.filters),
    isLoading: false,
    refetch: vi.fn(),
    isRefetching: false,
    loadMore,
    isLoadingMore: false,
    hasMore: false,
    truncated: false,
    lookedBackTo: undefined,
    ...hookOverrides,
  }) as unknown as ReturnType<typeof useTraceList>);
});

/** Renders the current URL search so tests can read it. */
function SearchProbe() {
  return <div data-testid="search">{useLocation().search}</div>;
}
const currentParams = () => new URLSearchParams(screen.getByTestId("search").textContent ?? "");

/** The traces page at the given search string. */
const pageTree = (search: string) => (
    <ThemeProvider theme={createTheme()}>
      <MemoryRouter initialEntries={[`/org/o/project/p/agents/a/environment/dev/traces${search}`]}>
        <Routes>
          <Route
            path="/org/:orgId/project/:projectId/agents/:agentId/environment/:envId/traces"
            element={
              <>
                <TracesComponent />
                <SearchProbe />
              </>
            }
          />
        </Routes>
      </MemoryRouter>
    </ThemeProvider>
);

/** Renders the traces page; rerenderPage re-reads hookOverrides, as a hook state change would. */
const renderPage = (search = "") => {
  const view = render(pageTree(search));
  return { ...view, rerenderPage: () => view.rerender(pageTree(search)) };
};

// hidden: an open drawer marks the page behind it aria-hidden.
const pickOption = (selectName: string, optionName: string) => {
  fireEvent.mouseDown(screen.getByRole("combobox", { name: selectName, hidden: true }));
  fireEvent.click(within(screen.getByRole("listbox")).getByRole("option", { name: optionName }));
};

describe("trace filter URL parsing", () => {
  it("keeps valid params and drops ones the API would reject", () => {
    expect(
      parseTraceFilters(
        new URLSearchParams(
          "status=error&minDurationMs=5000&minTokens=5k&minSpanCount=-1&model=%20gpt-4o%20&conversationId=",
        ),
      ),
    ).toEqual({ status: "error", minDurationMs: 5000, model: "gpt-4o" });
    expect(parseTraceFilters(new URLSearchParams("status=failed&minTokens=0"))).toEqual({
      minTokens: 0,
    });
    const atLimit = "a".repeat(256);
    expect(
      parseTraceFilters(new URLSearchParams({ model: atLimit, conversationId: `${atLimit}b` })),
    ).toEqual({ model: atLimit });
  });

  it("labels chips for people", () => {
    expect(
      traceFilterChips({ minDurationMs: 5000, minTokens: 10000, minSpanCount: 20 }).map(
        (c) => c.label,
      ),
    ).toEqual(["Latency ≥ 5s", "Tokens ≥ 10k", "Steps ≥ 20"]);
  });
});

describe("TracesComponent filters", () => {
  it("reproduces a pasted URL's filters in the request and the chips", () => {
    renderPage("?timeRange=1h&status=error&minDurationMs=5000&model=gpt-4o&minTokens=abc");

    expect(lastFilters()).toEqual({ status: "error", minDurationMs: 5000, model: "gpt-4o" });
    expect(screen.getByText("Status: Error", { selector: ".MuiChip-label" })).toBeInTheDocument();
    expect(screen.getByText("Latency ≥ 5s", { selector: ".MuiChip-label" })).toBeInTheDocument();
    expect(screen.getByText("Model: gpt-4o", { selector: ".MuiChip-label" })).toBeInTheDocument();
    expect(screen.queryByText(/^Tokens ≥/, { selector: ".MuiChip-label" })).not.toBeInTheDocument();
  });

  it("writes a picked filter to the URL under the API param name", () => {
    renderPage("?timeRange=1h&limit=20");

    pickOption("Latency ≥", "10s");
    pickOption("Tokens ≥", "5k");

    const params = currentParams();
    expect(params.get("minDurationMs")).toBe("10000");
    expect(params.get("minTokens")).toBe("5000");
    expect(params.get("timeRange")).toBe("1h");
    expect(params.get("limit")).toBe("20");
    expect(lastFilters()).toEqual({ minDurationMs: 10000, minTokens: 5000 });
  });

  it("commits text filters on Enter or blur, not per keystroke", () => {
    renderPage();

    const model = screen.getByRole("textbox", { name: "Model" });
    fireEvent.change(model, { target: { value: "gpt-4o" } });
    expect(currentParams().get("model")).toBeNull();
    fireEvent.keyDown(model, { key: "Enter" });
    expect(currentParams().get("model")).toBe("gpt-4o");

    const conversation = screen.getByRole("textbox", { name: "Conversation ID" });
    fireEvent.change(conversation, { target: { value: " conv-1 " } });
    fireEvent.blur(conversation);
    expect(currentParams().get("conversationId")).toBe("conv-1");
  });

  it("caps the text filters at the API's 256 characters", () => {
    renderPage();

    expect(screen.getByRole("textbox", { name: "Model" })).toHaveAttribute("maxLength", "256");
    expect(screen.getByRole("textbox", { name: "Conversation ID" })).toHaveAttribute(
      "maxLength",
      "256",
    );
  });

  it("removes only the chip's filter, and Clear all removes the rest", () => {
    renderPage("?timeRange=1h&status=error&minSpanCount=20&model=gpt-4o");

    fireEvent.click(screen.getByLabelText("Remove Steps ≥ 20"));
    let params = currentParams();
    expect(params.get("minSpanCount")).toBeNull();
    expect(params.get("status")).toBe("error");
    expect(params.get("model")).toBe("gpt-4o");

    fireEvent.click(screen.getByRole("button", { name: "Clear all" }));
    params = currentParams();
    expect(params.get("status")).toBeNull();
    expect(params.get("model")).toBeNull();
    expect(params.get("timeRange")).toBe("1h");
    expect(lastFilters()).toEqual({});
  });

  it("hides Clear all with a single active filter", () => {
    renderPage("?status=ok");
    expect(screen.getByText("Status: OK", { selector: ".MuiChip-label" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear all" })).not.toBeInTheDocument();
  });

  it("keeps the drawer open when its trace is still in the filtered list", () => {
    renderPage("?selectedTrace=t-err");
    pickOption("Status", "Error");
    expect(currentParams().get("selectedTrace")).toBe("t-err");
  });

  it("closes the drawer when a filter drops its trace", () => {
    renderPage("?selectedTrace=t-ok");
    pickOption("Status", "Error");
    expect(currentParams().get("status")).toBe("error");
    expect(currentParams().get("selectedTrace")).toBeNull();
  });
});

describe("TracesComponent trace ID search", () => {
  it("opens the trace on Enter, trimmed and lowercased, keeping the other params", () => {
    renderPage("?timeRange=1h&status=error");

    const input = screen.getByRole("textbox", { name: "Go to trace ID" });
    fireEvent.change(input, { target: { value: "  ABC123  " } });
    expect(currentParams().get("selectedTrace")).toBeNull();
    fireEvent.keyDown(input, { key: "Enter" });

    const params = currentParams();
    expect(params.get("selectedTrace")).toBe("abc123");
    expect(params.get("timeRange")).toBe("1h");
    expect(params.get("status")).toBe("error");
  });

  it("opens the trace from the button", () => {
    renderPage();

    fireEvent.change(screen.getByRole("textbox", { name: "Go to trace ID" }), {
      target: { value: "abc123" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Go to trace" }));

    expect(currentParams().get("selectedTrace")).toBe("abc123");
  });

  it("does nothing for a blank ID", () => {
    renderPage();

    const input = screen.getByRole("textbox", { name: "Go to trace ID" });
    fireEvent.change(input, { target: { value: "   " } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.click(screen.getByRole("button", { name: "Go to trace" }));

    expect(currentParams().get("selectedTrace")).toBeNull();
  });

  it("reads the trace through the spans lookup over the page's window, not the list", () => {
    renderPage();

    fireEvent.change(screen.getByRole("textbox", { name: "Go to trace ID" }), {
      target: { value: "not-in-the-list" },
    });
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Go to trace ID" }), { key: "Enter" });

    expect(vi.mocked(useTrace).mock.lastCall).toEqual([
      "ns",
      "p",
      "a",
      "dev",
      "not-in-the-list",
      "2026-10-01T09:00:00Z",
      "2026-10-01T11:00:00Z",
    ]);
    expect(lastFilters()).toEqual({});
  });
});

describe("trace column URL parsing", () => {
  it("defaults to Conversation, drops unknown columns, and reads empty as none", () => {
    expect(parseTraceColumns(new URLSearchParams(""))).toEqual(["conversation"]);
    expect(parseTraceColumns(new URLSearchParams("columns=model,bogus,conversation"))).toEqual([
      "conversation",
    ]);
    expect(parseTraceColumns(new URLSearchParams("columns=conversation,traceId"))).toEqual([
      "traceId",
      "conversation",
    ]);
    expect(parseTraceColumns(new URLSearchParams("columns="))).toEqual([]);
  });
});

describe("TracesComponent columns and cap notice", () => {
  const columnHeader = (name: string) =>
    screen.queryByRole("columnheader", { name, hidden: true });
  const openColumnsMenu = () => fireEvent.click(screen.getByRole("button", { name: "Columns" }));

  it("has no Model column and leaves models to the server's model filter", () => {
    const { unmount } = renderPage("?timeRange=1h");
    expect(columnHeader("Conversation")).toBeInTheDocument();
    expect(columnHeader("Model")).not.toBeInTheDocument();
    expect(lastIncludeModels()).toBeUndefined();
    unmount();

    renderPage("?timeRange=1h&model=gpt-4o");
    expect(columnHeader("Model")).not.toBeInTheDocument();
    expect(lastFilters()).toEqual({ model: "gpt-4o" });
    expect(lastIncludeModels()).toBeUndefined();
    expect(screen.queryByText("gpt-4o")).not.toBeInTheDocument();
  });

  it("hides Conversation from the menu, which lists no Model item", () => {
    renderPage("?timeRange=1h");

    openColumnsMenu();
    expect(screen.getAllByRole("menuitemcheckbox").map((el) => el.textContent)).toEqual([
      "Trace ID",
      "Conversation",
    ]);
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Conversation" }));

    expect(currentParams().get("columns")).toBe("");
    expect(currentParams().get("timeRange")).toBe("1h");
    expect(columnHeader("Conversation")).not.toBeInTheDocument();
  });

  it("hides the Trace ID column by default and shows it from the menu", () => {
    renderPage("?timeRange=1h");

    expect(columnHeader("Trace ID")).not.toBeInTheDocument();
    expect(screen.queryByText("t-err")).not.toBeInTheDocument();

    openColumnsMenu();
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Trace ID" }));

    expect(currentParams().get("columns")).toBe("traceId,conversation");
    expect(columnHeader("Trace ID")).toBeInTheDocument();
    expect(screen.getByText("t-err")).toBeInTheDocument();
    expect(screen.getByText("t-ok")).toBeInTheDocument();
  });

  it("copies the trace ID from its cell without opening the trace", async () => {
    renderPage("?timeRange=1h&columns=traceId");

    fireEvent.click(screen.getByRole("button", { name: "Copy trace ID t-ok" }));

    await waitFor(() => expect(copyToClipboard).toHaveBeenCalledWith("t-ok"));
    expect(currentParams().get("selectedTrace")).toBeNull();
  });

  it("reads an older columns link that still lists model", () => {
    renderPage("?columns=conversation,model");

    expect(columnHeader("Conversation")).toBeInTheDocument();
    expect(columnHeader("Model")).not.toBeInTheDocument();
    expect(lastIncludeModels()).toBeUndefined();
  });

  it("sets the conversation filter from the column without opening the trace", () => {
    renderPage("?timeRange=1h");

    fireEvent.click(screen.getByRole("button", { name: "Filter by conversation conv-1" }));

    expect(currentParams().get("conversationId")).toBe("conv-1");
    expect(currentParams().get("timeRange")).toBe("1h");
    expect(currentParams().get("selectedTrace")).toBeNull();
    expect(lastFilters()).toEqual({ conversationId: "conv-1" });
  });

  it("shows the examine-cap banner only when the page was truncated", () => {
    const { unmount } = renderPage();
    expect(screen.queryByText(/traces searched so far/)).not.toBeInTheDocument();
    unmount();

    hookOverrides = { truncated: true, hasMore: true };
    renderPage("?status=error");
    expect(
      screen.getByText(
        "Showing matches from the traces searched so far. Narrow the time range to see more.",
      ),
    ).toBeInTheDocument();
  });

  it("says the list stops here when truncated without a next page", () => {
    hookOverrides = { truncated: true, hasMore: false };
    renderPage("?status=error");

    expect(screen.queryByText(/traces searched so far/)).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "The list stops here: the remaining traces are more than 1,000 traces into this time range. Narrow the time range to see more.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load More Traces" })).not.toBeInTheDocument();
  });

  it("shows how far a filtered list searched in the row after the traces", () => {
    hookOverrides = { hasMore: true, lookedBackTo: "2026-10-01T08:14:00Z" };
    const { unmount } = renderPage();
    expect(screen.queryByText(/Searched as far as/)).not.toBeInTheDocument();
    unmount();

    renderPage("?status=error");
    const sentinelRow = screen.getByTestId("traces-sentinel").closest("tr")!;
    expect(
      within(sentinelRow).getByText(/^Searched as far as \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/),
    ).toBeInTheDocument();
  });
});

describe("TracesComponent export warning", () => {
  const LOOKED_BACK_TO = "2026-10-01T08:14:00Z";
  const SPANS_TEXT =
    "Some traces have more than 10,000 spans. The file has the first 10,000 spans of each.";
  const FAILED_3_TEXT =
    "3 traces couldn't be read, so the file may be missing them. Export again to retry.";
  const exportTraces = vi.fn<(params: unknown) => Promise<TraceExportResponse>>();
  const createObjectURL = vi.fn(() => "blob:export");
  let clickSpy: ReturnType<typeof vi.spyOn>;

  const exportResponse = (extra: Partial<TraceExportResponse> = {}): TraceExportResponse => ({
    traces: [],
    totalCount: 0,
    truncated: false,
    spansTruncated: false,
    ...extra,
  });
  const clickExport = () => fireEvent.click(screen.getByRole("button", { name: "Export" }));

  beforeEach(() => {
    exportTraces.mockReset();
    vi.mocked(useExportTraces).mockReturnValue({
      mutateAsync: exportTraces,
      isPending: false,
    } as unknown as ReturnType<typeof useExportTraces>);
    // jsdom has no object URLs and would try to navigate on the link click.
    Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() });
    clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  });

  afterEach(() => {
    clickSpy.mockRestore();
    vi.useRealTimers();
  });

  it("says a filtered export stopped early and how far it searched", async () => {
    exportTraces.mockResolvedValue(
      exportResponse({ truncated: true, lookedBackTo: LOOKED_BACK_TO }),
    );
    renderPage("?status=error");

    clickExport();

    expect(
      await screen.findByText(
        "The export stopped before the end of the time range. " +
          `It searched as far as ${formatStartTime(LOOKED_BACK_TO)}. ` +
          "Narrow the time range to export the rest.",
      ),
    ).toBeInTheDocument();
    expect(formatStartTime(LOOKED_BACK_TO)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
    expect(exportTraces).toHaveBeenCalledWith(expect.objectContaining({ filters: { status: "error" } }));
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(SPANS_TEXT)).not.toBeInTheDocument();
  });

  it("leaves out how far it searched when lookedBackTo is missing", async () => {
    exportTraces.mockResolvedValue(exportResponse({ truncated: true }));
    renderPage("?status=error");

    clickExport();

    expect(
      await screen.findByText(
        "The export stopped before the end of the time range. Narrow the time range to export the rest.",
      ),
    ).toBeInTheDocument();
    expect(createObjectURL).toHaveBeenCalledTimes(1);
  });

  it("says spans were cut, without a search line when no filter is set", async () => {
    exportTraces.mockResolvedValue(exportResponse({ truncated: true, spansTruncated: true }));
    renderPage();

    clickExport();

    expect(await screen.findByText(SPANS_TEXT)).toBeInTheDocument();
    expect(screen.queryByText(/stopped/)).not.toBeInTheDocument();
    expect(createObjectURL).toHaveBeenCalledTimes(1);
  });

  it("says spans were cut and the search may also have stopped early with a filter set", async () => {
    exportTraces.mockResolvedValue(
      exportResponse({ truncated: true, spansTruncated: true, lookedBackTo: LOOKED_BACK_TO }),
    );
    renderPage("?status=error");

    clickExport();

    expect(await screen.findByText(SPANS_TEXT)).toBeInTheDocument();
    expect(
      screen.getByText(
        `The search may also have stopped early: it searched as far as ${formatStartTime(LOOKED_BACK_TO)}.`,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/export stopped before/)).not.toBeInTheDocument();
    expect(createObjectURL).toHaveBeenCalledTimes(1);
  });

  it("shows no warning when the export is complete", async () => {
    exportTraces.mockResolvedValue(exportResponse({ lookedBackTo: LOOKED_BACK_TO }));
    renderPage("?status=error");

    clickExport();

    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the warning until it is closed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    exportTraces.mockResolvedValue(exportResponse({ truncated: true, spansTruncated: true }));
    renderPage();

    clickExport();
    await screen.findByText(SPANS_TEXT);
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    fireEvent.click(document.body);
    expect(screen.getByText(SPANS_TEXT)).toBeInTheDocument();

    fireEvent.click(within(screen.getByRole("alert")).getByRole("button", { name: "Close" }));
    expect(screen.queryByText(SPANS_TEXT)).not.toBeInTheDocument();
  });

  it("clears the warning when a new export starts", async () => {
    let finishSecond!: (resp: TraceExportResponse) => void;
    exportTraces
      .mockResolvedValueOnce(exportResponse({ truncated: true, spansTruncated: true }))
      .mockReturnValueOnce(new Promise((resolve) => (finishSecond = resolve)));
    renderPage();

    clickExport();
    await screen.findByText(SPANS_TEXT);
    clickExport();

    expect(exportTraces).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(SPANS_TEXT)).not.toBeInTheDocument();
    await act(async () => finishSecond(exportResponse()));
    expect(createObjectURL).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(SPANS_TEXT)).not.toBeInTheDocument();
  });

  it("says how many traces couldn't be read, without filters", async () => {
    exportTraces.mockResolvedValue(
      exportResponse({ failedTraceIds: ["trace-1", "trace-2", "trace-3"] }),
    );
    renderPage();

    clickExport();

    expect(await screen.findByText(FAILED_3_TEXT)).toBeInTheDocument();
    expect(createObjectURL).toHaveBeenCalledTimes(1);
  });

  it("says one trace couldn't be read in the singular", async () => {
    exportTraces.mockResolvedValue(exportResponse({ failedTraceIds: ["trace-1"] }));
    renderPage("?status=error");

    clickExport();

    expect(
      await screen.findByText(
        "1 trace couldn't be read, so the file may be missing it. Export again to retry.",
      ),
    ).toBeInTheDocument();
  });

  it("stacks the unread line under the cut-spans line", async () => {
    exportTraces.mockResolvedValue(
      exportResponse({
        truncated: true,
        spansTruncated: true,
        failedTraceIds: ["trace-1", "trace-2", "trace-3"],
      }),
    );
    renderPage();

    clickExport();

    expect(await screen.findByText(SPANS_TEXT)).toBeInTheDocument();
    expect(screen.getByText(FAILED_3_TEXT)).toBeInTheDocument();
  });

  it("stacks the unread line under the stopped-search line", async () => {
    exportTraces.mockResolvedValue(
      exportResponse({
        truncated: true,
        lookedBackTo: LOOKED_BACK_TO,
        failedTraceIds: ["trace-1", "trace-2", "trace-3"],
      }),
    );
    renderPage("?status=error");

    clickExport();

    expect(await screen.findByText(/^The export stopped before the end/)).toBeInTheDocument();
    expect(screen.getByText(FAILED_3_TEXT)).toBeInTheDocument();
  });

  it("shows no warning for an empty unread list", async () => {
    exportTraces.mockResolvedValue(exportResponse({ failedTraceIds: [] }));
    renderPage();

    clickExport();

    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("TracesComponent infinite scroll", () => {
  const loadMoreButton = () => screen.queryByRole("button", { name: "Load More Traces" });
  // Lets a resolved loadMore result reach the table.
  const settleLoad = () => act(async () => {});

  it("asks the hook to keep loaded pages through focus and reconnect", () => {
    renderPage();
    expect(mockUseTraceList.mock.lastCall?.[9]?.paged).toBe(true);
  });

  it("loads the next page once when the sentinel comes into view", () => {
    hookOverrides = { hasMore: true };
    renderPage();
    expect(loadMoreButton()).not.toBeInTheDocument();

    scrollToSentinel();
    scrollToSentinel();
    expect(loadMore).toHaveBeenCalledTimes(1);
  });

  it("doesn't load while a page is loading or when there are no more pages", () => {
    hookOverrides = { hasMore: true, isLoadingMore: true };
    const { unmount } = renderPage();
    const sentinelRow = screen.getByTestId("traces-sentinel").closest("tr")!;
    expect(within(sentinelRow).getByRole("progressbar")).toBeInTheDocument();
    scrollToSentinel();
    expect(loadMore).not.toHaveBeenCalled();
    unmount();

    hookOverrides = { hasMore: false };
    renderPage();
    scrollToSentinel();
    expect(loadMore).not.toHaveBeenCalled();
  });

  it("pauses on a page that adds no rows and resumes when a click adds some", async () => {
    hookOverrides = { hasMore: true };
    const { rerenderPage } = renderPage("?status=error");
    loadMore.mockResolvedValueOnce(false);
    scrollToSentinel();
    expect(loadMore).toHaveBeenCalledTimes(1);

    // The page lands with no new matches; the merge still hands over a new array.
    hookOverrides = { hasMore: true, isLoadingMore: true };
    rerenderPage();
    hookOverrides = { hasMore: true, data: { traces: [ALL[0]], totalCount: 1 } };
    rerenderPage();
    await settleLoad();
    expect(loadMoreButton()).toBeInTheDocument();
    scrollToSentinel();
    expect(loadMore).toHaveBeenCalledTimes(1);

    loadMore.mockResolvedValueOnce(true);
    fireEvent.click(loadMoreButton()!);
    expect(loadMore).toHaveBeenCalledTimes(2);

    // The clicked page adds a row, so scrolling loads again.
    const grown = [...ALL, makeTrace("t-new", 1)];
    hookOverrides = { hasMore: true, isLoadingMore: true };
    rerenderPage();
    hookOverrides = { hasMore: true, data: { traces: grown, totalCount: grown.length } };
    rerenderPage();
    await settleLoad();
    expect(loadMoreButton()).not.toBeInTheDocument();
    scrollToSentinel();
    expect(loadMore).toHaveBeenCalledTimes(3);
  });

  it("doesn't pause a list that resets while a page is loading", async () => {
    hookOverrides = { hasMore: true };
    const { rerenderPage } = renderPage("?status=error");
    scrollToSentinel();
    hookOverrides = { hasMore: true, isLoadingMore: true };
    rerenderPage();

    // A new list of the same length lands, and the old load resolves undefined.
    hookOverrides = { hasMore: true, data: { traces: [makeTrace("t-other")], totalCount: 1 } };
    rerenderPage();
    await settleLoad();

    expect(loadMoreButton()).not.toBeInTheDocument();
    scrollToSentinel();
    expect(loadMore).toHaveBeenCalledTimes(2);
  });

  it("resumes auto-loading when a new list replaces a paused one", async () => {
    hookOverrides = { hasMore: true };
    const { rerenderPage } = renderPage("?status=error");
    loadMore.mockResolvedValueOnce(false);
    scrollToSentinel();
    await settleLoad();
    expect(loadMoreButton()).toBeInTheDocument();

    hookOverrides = { hasMore: true, data: { traces: [makeTrace("t-other")], totalCount: 1 } };
    rerenderPage();
    expect(loadMoreButton()).not.toBeInTheDocument();
  });

  it("shows the button under an empty filtered page and doesn't auto-load", () => {
    hookOverrides = { hasMore: true, data: { traces: [], totalCount: 0 } };
    renderPage("?status=error");

    expect(screen.getByText("No traces found!")).toBeInTheDocument();
    scrollToSentinel();
    expect(loadMore).not.toHaveBeenCalled();
    fireEvent.click(loadMoreButton()!);
    expect(loadMore).toHaveBeenCalledTimes(1);
  });

  it("shows Retry after a failed load and stops auto-loading", () => {
    hookOverrides = { hasMore: true, loadError: new Error("upstream down") };
    renderPage();

    expect(screen.getByText("Couldn't load more traces.")).toBeInTheDocument();
    scrollToSentinel();
    expect(loadMore).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(loadMore).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["desc", "", "desc"],
    ["asc", "?sortOrder=asc", "asc"],
  ])("puts the sentinel after the last row in %s order", (_, search, sortOrder) => {
    hookOverrides = { hasMore: true };
    renderPage(search);

    expect(mockUseTraceList.mock.lastCall?.[6]).toBe(sortOrder);
    const lastRow = screen.getByText("root t-ok").closest("tr")!;
    const sentinel = screen.getByTestId("traces-sentinel");
    const position = lastRow.compareDocumentPosition(sentinel);
    expect(position & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(sentinel.closest("tr")).not.toBe(lastRow);
  });
});
