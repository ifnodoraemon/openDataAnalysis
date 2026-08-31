#!/usr/bin/env node
const fs = require("fs");
const path = require("path");
const ROOT = process.cwd();
const SCENARIO_ROOT = path.join(ROOT, "samples", "coverage_scenarios");
const OUTPUT_ROOT = path.join(ROOT, "tmp", "scenario-runs");
let activeTimelinePath = "";

function parseArgs(argv) {
  const args = {
    baseUrl: "http://127.0.0.1:8080",
    scenarioId: "",
    timeoutSec: 180,
  };
  for (let i = 2; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--id") args.scenarioId = requireArgValue(argv, ++i, arg);
    else if (arg === "--base-url")
      args.baseUrl = requireArgValue(argv, ++i, arg);
    else if (arg === "--timeout")
      args.timeoutSec = Number(requireArgValue(argv, ++i, arg));
    else if (arg === "--help" || arg === "-h") {
      console.log(
        "用法：node scripts/run_scenario.js --id <scenario_id> [--base-url http://127.0.0.1:8080] [--timeout 180]",
      );
      process.exit(0);
    } else {
      throw new Error(`未知参数：${arg}`);
    }
  }
  if (!args.scenarioId) {
    throw new Error("缺少 --id");
  }
  if (!Number.isFinite(args.timeoutSec) || args.timeoutSec <= 0) {
    throw new Error("--timeout 无效");
  }
  return args;
}

function requireArgValue(argv, index, flag) {
  const value = argv[index];
  if (!value || value.startsWith("--")) {
    throw new Error(`${flag} 缺少参数值`);
  }
  return value;
}

function serverOrigin(baseUrl) {
  let raw = String(baseUrl || "").trim();
  if (!/^https?:\/\//i.test(raw)) raw = `http://${raw}`;
  const parsed = new URL(raw);
  // Only default the port for plain HTTP; https origins keep 443.
  if (!parsed.port && parsed.protocol === "http:") parsed.port = "8080";
  parsed.pathname = "";
  parsed.search = "";
  parsed.hash = "";
  return parsed.toString().replace(/\/$/, "");
}

function loadEnvFile(filePath) {
  const env = {};
  const raw = fs.readFileSync(filePath, "utf8");
  for (const line of raw.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const idx = trimmed.indexOf("=");
    if (idx <= 0) continue;
    env[trimmed.slice(0, idx)] = trimmed.slice(idx + 1);
  }
  return env;
}

function listScenarioDirs() {
  return fs
    .readdirSync(SCENARIO_ROOT)
    .map((name) => path.join(SCENARIO_ROOT, name))
    .filter((p) => fs.existsSync(path.join(p, "scenario.yaml")));
}

function parseYamlScalar(raw) {
  const value = raw.trim();
  if (value === "true") return true;
  if (value === "false") return false;
  if (value === "null") return null;
  if (/^-?\d+(\.\d+)?$/.test(value)) return Number(value);
  if (
    (value.startsWith('"') && value.endsWith('"')) ||
    (value.startsWith("'") && value.endsWith("'"))
  ) {
    return value.slice(1, -1);
  }
  return value;
}

function parseSimpleYaml(text) {
  const lines = text.split(/\r?\n/);
  const root = {};
  const stack = [{ indent: -1, container: root }];

  function nextMeaningfulLine(start) {
    for (let i = start; i < lines.length; i += 1) {
      const raw = lines[i];
      const trimmed = raw.trim();
      if (!trimmed || trimmed.startsWith("#")) continue;
      return raw;
    }
    return null;
  }

  for (let i = 0; i < lines.length; i += 1) {
    const raw = lines[i];
    const trimmed = raw.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const indent = raw.match(/^\s*/)[0].length;

    while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
      stack.pop();
    }
    const parent = stack[stack.length - 1].container;

    if (trimmed.startsWith("- ")) {
      if (!Array.isArray(parent)) {
        throw new Error(`YAML 列表项无效：${trimmed}`);
      }
      parent.push(parseYamlScalar(trimmed.slice(2)));
      continue;
    }

    const idx = trimmed.indexOf(":");
    if (idx < 0) {
      throw new Error(`YAML 行无效：${trimmed}`);
    }
    const key = trimmed.slice(0, idx).trim();
    const rest = trimmed.slice(idx + 1).trim();

    if (rest) {
      parent[key] = parseYamlScalar(rest);
      continue;
    }

    const nextRaw = nextMeaningfulLine(i + 1);
    const nextTrimmed = nextRaw ? nextRaw.trim() : "";
    const nextIndent = nextRaw ? nextRaw.match(/^\s*/)[0].length : -1;
    const container =
      nextTrimmed.startsWith("- ") && nextIndent > indent ? [] : {};
    parent[key] = container;
    stack.push({ indent, container });
  }

  return root;
}

function loadScenarioById(id) {
  for (const dir of listScenarioDirs()) {
    const yamlPath = path.join(dir, "scenario.yaml");
    const data = parseSimpleYaml(fs.readFileSync(yamlPath, "utf8"));
    if (data.id === id || path.basename(dir) === id)
      return { dir, data, yamlPath };
  }
  throw new Error(`场景不存在：${id}`);
}

function nowStamp() {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
}

async function httpJson(url, init = {}) {
  const res = await fetch(url, init);
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`${res.status} ${res.statusText}: ${text}`);
  }
  if (!text) throw new Error(`${url} 返回了空 JSON 响应`);
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Error(`${url} 返回的 JSON 无效：${error.message}`);
  }
}

async function consumeEventStream(response, onEvent) {
  if (!response.body) throw new Error("事件流响应没有消息体");
  const decoder = new TextDecoder();
  let buffer = "";

  function consumeFrame(frame) {
    const data = frame
      .split(/\r?\n/)
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice(5).trimStart())
      .join("\n");
    if (!data) return;
    onEvent(JSON.parse(data));
  }

  for await (const chunk of response.body) {
    buffer += decoder.decode(chunk, { stream: true });
    const frames = buffer.split(/\r?\n\r?\n/);
    buffer = frames.pop() || "";
    for (const frame of frames) consumeFrame(frame);
  }
  buffer += decoder.decode();
  if (buffer.trim()) consumeFrame(buffer);
}

async function uploadFile(apiOrigin, token, sessionId, filePath) {
  const form = new FormData();
  const buf = fs.readFileSync(filePath);
  form.append(
    "file",
    new Blob([buf], { type: "text/csv" }),
    path.basename(filePath),
  );
  const res = await fetch(
    `${apiOrigin}/api/upload?session_id=${encodeURIComponent(sessionId)}`,
    {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
      body: form,
    },
  );
  const text = await res.text();
  if (!res.ok)
    throw new Error(
      `上传 ${path.basename(filePath)} 失败：${res.status} ${text}`,
    );
  if (!text) throw new Error(`上传 ${path.basename(filePath)} 返回了空响应`);
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Error(
      `上传 ${path.basename(filePath)} 返回的 JSON 无效：${error.message}`,
    );
  }
}

function summarizeEvents(events) {
  const toolCalls = events
    .filter((e) => e.type === "tool_call")
    .map((e) => e.data?.name)
    .filter(Boolean);
  const terminal = [...events]
    .reverse()
    .find((e) =>
      [
        "run_completed",
        "error",
        "user_request_input",
        "run_cancelled",
      ].includes(e.type),
    );
  const reportFinal = events.find((e) => e.type === "report_final");
  const asked = events.find((e) => e.type === "user_request_input");
  const err = events.find((e) => e.type === "error");
  const errorMessage = err?.data?.message || "";
  return {
    event_count: events.length,
    tool_calls: toolCalls,
    unique_tool_calls: [...new Set(toolCalls)],
    terminal_type: terminal?.type || "",
    terminal_payload: terminal?.data || null,
    asked_user: Boolean(asked),
    user_question: asked?.data?.question || "",
    has_report_final: Boolean(reportFinal),
    error_message: errorMessage,
    error_category: String(err?.data?.error_code || err?.data?.category || ""),
  };
}

function safeJsonParse(raw) {
  if (typeof raw !== "string") return null;
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

function clip(value, max = 240) {
  const text = String(value || "")
    .replace(/\s+/g, " ")
    .trim();
  if (text.length <= max) return text;
  return `${text.slice(0, max - 1)}…`;
}

function extractResultPreview(raw) {
  const parsed = safeJsonParse(raw);
  if (!parsed || typeof parsed !== "object") return clip(raw);
  return clip(
    parsed.ui_summary ||
      parsed.message ||
      parsed.error ||
      parsed.result ||
      parsed.delegate_summary ||
      raw,
  );
}

function formatTimelineLine(record) {
  const event = record.event || {};
  const data = event.data || {};
  const elapsed = `${String(record.elapsed_ms).padStart(7, " ")}ms`;
  switch (event.type) {
    case "run_started":
      return `${elapsed} run_started run_id=${data.runId || ""}`;
    case "assistant_status":
      return `${elapsed} assistant_status ${clip(data.content)}`;
    case "tool_call":
      return `${elapsed} tool_call ${data.name || ""} args=${clip(JSON.stringify(data.arguments || data.args || {}), 180)}`;
    case "tool_result":
      return `${elapsed} tool_result ${data.name || ""} success=${data.success} duration_ms=${data.duration ?? ""} ${extractResultPreview(data.result)}`;
    case "user_request_input":
      return `${elapsed} user_request_input ${clip(data.question || data.content || "")}`;
    case "report_final":
      return `${elapsed} report_final title=${clip(data.title || "")}`;
    case "run_completed":
      return `${elapsed} run_completed ${clip(data.summary)}`;
    case "run_cancelled":
      return `${elapsed} run_cancelled ${clip(data.message || "")}`;
    case "error":
      return `${elapsed} error ${clip(data.message || data.error || "")}`;
    default:
      return `${elapsed} ${event.type || "event"} ${clip(JSON.stringify(data), 220)}`;
  }
}

function appendTrace(outDir, record) {
  fs.appendFileSync(
    path.join(outDir, "events.ndjson"),
    `${JSON.stringify(record)}\n`,
  );
  fs.appendFileSync(
    path.join(outDir, "timeline.log"),
    `${formatTimelineLine(record)}\n`,
  );
}

function appendTimelineNote(outDir, startedAt, label, message) {
  const record = {
    at: new Date().toISOString(),
    elapsed_ms: Date.now() - startedAt,
    event: { type: label, data: { message } },
  };
  appendTrace(outDir, record);
}

function readTail(filePath, lineCount = 80) {
  if (!filePath || !fs.existsSync(filePath)) return "";
  const lines = fs.readFileSync(filePath, "utf8").trimEnd().split(/\r?\n/);
  return lines.slice(-lineCount).join("\n");
}

function printTimelineTail(filePath, lineCount = 80) {
  const tail = readTail(filePath, lineCount);
  if (!tail) return;
  console.error("--- 场景时间线末尾 ---");
  console.error(tail);
  console.error("--- 场景时间线结束 ---");
}

function collectEvidence(events) {
  const toolCalls = [];
  const toolResults = [];

  for (const event of events) {
    if (event.type === "tool_call" && event.data?.name) {
      toolCalls.push(event.data.name);
      continue;
    }

    if (event.type !== "tool_result") continue;

    const parsed = safeJsonParse(event.data?.result || "");
    if (!parsed || typeof parsed !== "object") continue;

    toolResults.push({
      name: event.data?.name || parsed.tool || "",
      payload: parsed,
    });
  }

  return {
    uniqueToolCalls: [...new Set(toolCalls)],
    toolResults,
  };
}

function matchRequiredToolCall(name, evidence) {
  return evidence.uniqueToolCalls.includes(name);
}

function matchToolResultCode(spec, evidence) {
  const idx = String(spec || "").indexOf(":");
  if (idx <= 0) return false;
  const toolName = spec.slice(0, idx).trim();
  const errorCode = spec.slice(idx + 1).trim();
  if (!toolName || !errorCode) return false;
  return evidence.toolResults.some(({ name, payload }) => {
    const actualTool = String(name || payload?.tool || "").trim();
    const actualCode = String(payload?.error_code || "").trim();
    return actualTool === toolName && actualCode === errorCode;
  });
}

function evaluateScenario(scenario, events, summary) {
  const acceptance = scenario.data.acceptance || {};
  const evidence = collectEvidence(events);
  const checks = [];

  function addCheck(name, pass, details) {
    checks.push({ name, pass, details });
  }

  const terminalTypes = acceptance.terminal_types || [];
  if (terminalTypes.length > 0) {
    addCheck(
      "terminal_type",
      terminalTypes.includes(summary.terminal_type),
      `allowed=${terminalTypes.join(",")} actual=${summary.terminal_type}`,
    );
  }

  if (typeof acceptance.report_finalized === "boolean") {
    addCheck(
      "report_finalized",
      summary.has_report_final === acceptance.report_finalized,
      `configured=${acceptance.report_finalized} actual=${summary.has_report_final}`,
    );
  }

  if (acceptance.no_error === true) {
    addCheck(
      "no_error",
      !summary.error_message,
      summary.error_message || "none",
    );
  }

  for (const toolName of acceptance.required_tool_calls || []) {
    addCheck(
      `required_tool_call:${toolName}`,
      matchRequiredToolCall(toolName, evidence),
      toolName,
    );
  }

  for (const spec of acceptance.required_tool_result_codes || []) {
    addCheck(
      `required_tool_result_code:${spec}`,
      matchToolResultCode(spec, evidence),
      spec,
    );
  }

  const failed = checks.filter((item) => !item.pass);
  return {
    pass: failed.length === 0,
    failed_count: failed.length,
    checks,
    failed_checks: failed,
  };
}

async function main() {
  const args = parseArgs(process.argv);
  const apiOrigin = serverOrigin(args.baseUrl);
  const env = loadEnvFile(path.join(ROOT, "server", ".env"));
  const loginEmail = env.DEFAULT_USER_EMAIL;
  const loginPassword = env.DEFAULT_USER_PASSWORD;
  const workspaceId = env.DEFAULT_WORKSPACE_ID;
  if (!loginEmail || !loginPassword || !workspaceId) {
    throw new Error("server/.env 缺少默认登录凭据");
  }

  const scenario = loadScenarioById(args.scenarioId);
  const outDir = path.join(OUTPUT_ROOT, `${nowStamp()}-${scenario.data.id}`);
  fs.mkdirSync(outDir, { recursive: true });
  activeTimelinePath = path.join(outDir, "timeline.log");
  const startedAt = Date.now();
  appendTimelineNote(
    outDir,
    startedAt,
    "scenario_started",
    `id=${scenario.data.id}`,
  );

  const login = await httpJson(`${apiOrigin}/api/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      email: loginEmail,
      password: loginPassword,
      workspaceId,
    }),
  });
  const token = login.token;
  if (!token) throw new Error("登录响应没有 token");
  appendTimelineNote(
    outDir,
    startedAt,
    "login_completed",
    `email=${loginEmail}`,
  );

  const created = await httpJson(`${apiOrigin}/api/sessions`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
  const sessionId = created?.session?.id;
  if (!sessionId) throw new Error("创建会话失败");
  appendTimelineNote(
    outDir,
    startedAt,
    "session_created",
    `session_id=${sessionId}`,
  );

  const uploads = [];
  for (const rel of scenario.data.files || []) {
    const fullPath = path.join(scenario.dir, rel);
    uploads.push(await uploadFile(apiOrigin, token, sessionId, fullPath));
    appendTimelineNote(
      outDir,
      startedAt,
      "file_uploaded",
      path.basename(fullPath),
    );
  }

  const events = [];
  let runId = "";
  let doneResolve;
  let doneReject;
  const done = new Promise((resolve, reject) => {
    doneResolve = resolve;
    doneReject = reject;
  });
  const timer = setTimeout(() => {
    appendTimelineNote(
      outDir,
      startedAt,
      "scenario_timeout",
      `after ${args.timeoutSec}s`,
    );
    doneReject(new Error(`场景运行超过 ${args.timeoutSec} 秒`));
  }, args.timeoutSec * 1000);

  const streamController = new AbortController();
  const streamURL = `${apiOrigin}/api/sse?token=${encodeURIComponent(token)}&session_id=${encodeURIComponent(sessionId)}`;
  const streamResponse = await fetch(streamURL, {
    headers: { Accept: "text/event-stream" },
    signal: streamController.signal,
  });
  if (!streamResponse.ok) {
    const body = await streamResponse.text();
    throw new Error(`事件流连接失败：${streamResponse.status} ${body}`);
  }

  const handleRuntimeEvent = (event) => {
    events.push(event);
    appendTrace(outDir, {
      at: new Date().toISOString(),
      elapsed_ms: Date.now() - startedAt,
      event,
    });
    if (process.env.SCENARIO_TRACE === "1") {
      console.error(
        formatTimelineLine({ elapsed_ms: Date.now() - startedAt, event }),
      );
    }
    if (event.type === "run_started" && event.data?.runId) {
      runId = event.data.runId;
    }
    if (
      [
        "run_completed",
        "error",
        "user_request_input",
        "run_cancelled",
      ].includes(event.type)
    ) {
      doneResolve();
    }
  };
  let streamFailure = null;
  const streamTask = consumeEventStream(
    streamResponse,
    handleRuntimeEvent,
  ).catch((error) => {
    if (streamController.signal.aborted && error.name === "AbortError") return;
    streamFailure = error;
    appendTimelineNote(outDir, startedAt, "event_stream_error", error.message);
    doneReject(error);
  });

  try {
    const chat = await httpJson(
      `${apiOrigin}/api/sessions/${encodeURIComponent(sessionId)}/chat`,
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ content: scenario.data.prompt }),
      },
    );
    if (!chat?.run_id) throw new Error("对话响应没有 run_id");
    runId = chat.run_id;
    await done;
  } finally {
    clearTimeout(timer);
    streamController.abort();
    await streamTask;
  }
  if (streamFailure) throw streamFailure;

  let runData = null;
  let reportHtml = "";
  let reportFetch = null;
  if (runId) {
    try {
      runData = await httpJson(
        `${apiOrigin}/api/runs/${encodeURIComponent(runId)}`,
        {
          headers: { Authorization: `Bearer ${token}` },
        },
      );
    } catch (err) {
      runData = { fetch_error: String(err) };
    }
    try {
      const res = await fetch(
        `${apiOrigin}/api/runs/${encodeURIComponent(runId)}/report`,
        {
          headers: { Authorization: `Bearer ${token}` },
        },
      );
      const body = await res.text();
      reportFetch = { status: res.status, ok: res.ok };
      if (res.ok) reportHtml = body;
      else reportFetch.body = body;
    } catch (error) {
      reportFetch = { fetch_error: String(error) };
    }
  }

  const summary = summarizeEvents(events);
  const evaluation = evaluateScenario(scenario, events, summary);
  const result = {
    scenario_id: scenario.data.id,
    scenario_dir: path.relative(ROOT, scenario.dir),
    prompt: scenario.data.prompt,
    files: scenario.data.files,
    acceptance: scenario.data.acceptance || {},
    manual_review: scenario.data.manual_review || [],
    session_id: sessionId,
    run_id: runId,
    uploads,
    report_fetch: reportFetch,
    summary,
    evaluation,
  };

  fs.writeFileSync(
    path.join(outDir, "summary.json"),
    JSON.stringify(result, null, 2),
  );
  fs.writeFileSync(
    path.join(outDir, "events.json"),
    JSON.stringify(events, null, 2),
  );
  if (runData)
    fs.writeFileSync(
      path.join(outDir, "run.json"),
      JSON.stringify(runData, null, 2),
    );
  if (reportHtml)
    fs.writeFileSync(path.join(outDir, "report.html"), reportHtml);

  console.log(
    JSON.stringify(
      {
        out_dir: path.relative(ROOT, outDir),
        timeline: path.relative(ROOT, activeTimelinePath),
        ...summary,
        run_id: runId,
        evaluation: {
          pass: evaluation.pass,
          failed_count: evaluation.failed_count,
          failed_checks: evaluation.failed_checks.map((item) => item.name),
        },
      },
      null,
      2,
    ),
  );

  if (!evaluation.pass) {
    printTimelineTail(activeTimelinePath);
    process.exit(2);
  }
}

main().catch((err) => {
  printTimelineTail(activeTimelinePath);
  const message = err && err.message ? err.message : String(err);
  console.error(`错误：${message}`);
  process.exit(1);
});
