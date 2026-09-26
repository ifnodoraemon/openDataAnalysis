// @vitest-environment jsdom
import { createPinia, setActivePinia } from "pinia";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createApp, nextTick } from "vue";

vi.mock("../../composables/useAgentTransport.js", () => ({
  useAgentTransport: () => ({
    sendMessage: vi.fn(),
    stop: vi.fn(),
    ensureSession: vi.fn(async () => "sess_test"),
  }),
}));
vi.mock("../datasource/DataSourceModal.vue", () => ({
  default: { name: "DataSourceModal", template: "<div />" },
}));

import InputBar from "./InputBar.vue";
import { useAgentStore } from "../../stores/agent.js";

global.localStorage = {
  getItem: () => "test-token",
  setItem: () => {},
  removeItem: () => {},
  clear: () => {},
};

function jsonResponse(body, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify(body),
    json: async () => body,
  };
}

function mockFetch(routes) {
  global.fetch = vi.fn(async (url) => {
    const u = String(url);
    for (const [pattern, responder] of routes) {
      if (u.includes(pattern)) return responder();
    }
    throw new Error(`unexpected fetch: ${u}`);
  });
}

function mountInputBar() {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const app = createApp(InputBar);
  const pinia = createPinia();
  // One pinia for both the app and this test file's useAgentStore() calls.
  setActivePinia(pinia);
  app.use(pinia);
  app.mount(el);
  return {
    el,
    unmount() {
      app.unmount();
      el.remove();
    },
  };
}

async function flush() {
  await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
  await nextTick();
  await nextTick();
}

async function uploadFile(el, name) {
  const fileInput = el.querySelector('input[type="file"]');
  Object.defineProperty(fileInput, "files", {
    value: [new File(["data"], name, { type: "application/octet-stream" })],
    configurable: true,
  });
  fileInput.dispatchEvent(new Event("change"));
  await flush();
}

function messageTexts(store) {
  return store.messages.map((m) => `${m.type}:${m.content}`).join("\n");
}

describe("InputBar upload / worksheet-picker branches", () => {
  let store;
  let mounted;

  beforeEach(() => {
    store = null;
    mounted = null;
  });

  afterEach(() => {
    if (mounted) mounted.unmount();
    global.fetch = undefined;
    vi.restoreAllMocks();
  });

  it("worksheet_selection_required queues the picker and imports on click", async () => {
    mockFetch([
      ["/api/upload", () => jsonResponse({
        ingest_status: "worksheet_selection_required",
        source_id: "src_1",
        worksheets: ["Sheet1", "Sheet2"],
      })],
      ["/import", () => jsonResponse({ ingest_status: "success" })],
      ["/api/sessions/sess_test/sources", () => jsonResponse({ sources: [] })],
    ]);
    mounted = mountInputBar();
    store = useAgentStore();
    await uploadFile(mounted.el, "book.xlsx");

    // worksheet bar rendered with both sheets
    const chips = [...mounted.el.querySelectorAll(".worksheet-chip")];
    expect(chips.map((c) => c.textContent.trim())).toContain("Sheet1");
    expect(messageTexts(store)).toContain("user:📎 book.xlsx 已上传（包含多个工作表，请选择要导入的表）");

    // pick Sheet1 → server imports it → success message, queue drained
    chips.find((c) => c.textContent.trim() === "Sheet1").click();
    await flush();
    expect(messageTexts(store)).toContain("user:📎 已导入工作表「Sheet1」（book.xlsx）");
    expect(mounted.el.querySelector(".worksheet-bar")).toBeNull();
  });

  it("pickWorksheet rejects a 2xx response without an explicit success status", async () => {
    mockFetch([
      ["/api/upload", () => jsonResponse({
        ingest_status: "worksheet_selection_required",
        source_id: "src_1",
        worksheets: ["Sheet1"],
      })],
      // drifted contract: 200 OK but no ingest_status → must NOT show 已导入
      ["/import", () => jsonResponse({ message: "weird status" })],
      ["/api/sessions/sess_test/sources", () => jsonResponse({ sources: [] })],
    ]);
    mounted = mountInputBar();
    store = useAgentStore();
    await uploadFile(mounted.el, "book.xlsx");

    const chip = [...mounted.el.querySelectorAll(".worksheet-chip")]
      .find((c) => c.textContent.trim() === "Sheet1");
    chip.click();
    await flush();

    const texts = messageTexts(store);
    expect(texts).not.toContain("user:📎 已导入工作表「Sheet1」（book.xlsx）");
    expect(texts).toContain("error:导入工作表失败（Sheet1）: weird status");
    // picker stays for retry
    expect(mounted.el.querySelector(".worksheet-bar")).not.toBeNull();
  });

  it("needs_agent uploads queue the agent handoff bar", async () => {
    mockFetch([
      ["/api/upload", () => jsonResponse({
        ingest_status: "needs_agent",
        source_id: "src_9",
        import_error: "Excel row 7 has 5 cells; header has 4",
      })],
      ["/api/sessions/sess_test/sources", () => jsonResponse({ sources: [] })],
    ]);
    mounted = mountInputBar();
    store = useAgentStore();
    await uploadFile(mounted.el, "messy.xlsx");

    const texts = messageTexts(store);
    expect(texts).toContain("user:📎 messy.xlsx 已上传（结构较复杂，无法直接导入）");
    // handoff + dismiss buttons rendered
    const buttons = [...mounted.el.querySelectorAll(".worksheet-chip")];
    expect(buttons.some((b) => b.textContent.includes("让智能体"))).toBe(true);
  });

  it("failed ingest_status surfaces the server message as an error", async () => {
    mockFetch([
      ["/api/upload", () => jsonResponse({
        ingest_status: "failed",
        message: "文件已上传，但导入失败",
      })],
      ["/api/sessions/sess_test/sources", () => jsonResponse({ sources: [] })],
    ]);
    mounted = mountInputBar();
    store = useAgentStore();
    await uploadFile(mounted.el, "broken.csv");

    expect(messageTexts(store)).toContain(
      "error:数据源添加失败（broken.csv）: 文件已上传，但导入失败",
    );
  });

  it("a plain successful upload adds the data-source message", async () => {
    mockFetch([
      ["/api/upload", () => jsonResponse({ source_id: "src_2" })],
      ["/api/sessions/sess_test/sources", () => jsonResponse({ sources: [] })],
    ]);
    mounted = mountInputBar();
    store = useAgentStore();
    await uploadFile(mounted.el, "clean.csv");

    expect(messageTexts(store)).toContain("user:📎 已添加数据源: clean.csv (4 B)");
  });

  it("agentHandoff appends to an in-progress draft instead of replacing it", async () => {
    mockFetch([]);
    mounted = mountInputBar();
    store = useAgentStore();
    const textarea = mounted.el.querySelector("textarea");
    textarea.value = "我的草稿";
    textarea.dispatchEvent(new Event("input"));
    await nextTick();

    store.agentHandoff = { sourceId: "src_1", filename: "book.xlsx" };
    await nextTick();

    expect(textarea.value).toContain("我的草稿");
    expect(textarea.value).toContain("请读取数据源 src_1");
    expect(store.agentHandoff).toBeNull();
  });
});
