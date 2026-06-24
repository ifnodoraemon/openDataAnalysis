#!/usr/bin/env node
const fs = require('fs');
const path = require('path');
const ROOT = process.cwd();
const SCENARIO_ROOT = path.join(ROOT, 'samples', 'coverage_scenarios');
const OUTPUT_ROOT = path.join(ROOT, 'tmp', 'scenario-runs');
let activeTimelinePath = '';

function parseArgs(argv) {
  const args = { baseUrl: 'http://127.0.0.1:8080', scenarioId: '', timeoutSec: 180 };
  for (let i = 2; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--id') args.scenarioId = requireArgValue(argv, ++i, arg);
    else if (arg === '--base-url') args.baseUrl = requireArgValue(argv, ++i, arg);
    else if (arg === '--timeout') args.timeoutSec = Number(requireArgValue(argv, ++i, arg));
    else if (arg === '--help' || arg === '-h') {
      console.log('Usage: node scripts/run_scenario.js --id <scenario_id> [--base-url http://127.0.0.1:8080] [--timeout 180]');
      process.exit(0);
    } else {
      throw new Error(`unknown argument: ${arg}`);
    }
  }
  if (!args.scenarioId) {
    throw new Error('missing --id');
  }
  if (!Number.isFinite(args.timeoutSec) || args.timeoutSec <= 0) {
    throw new Error('invalid --timeout');
  }
  return args;
}

function requireArgValue(argv, index, flag) {
  const value = argv[index];
  if (!value || value.startsWith('--')) {
    throw new Error(`missing value for ${flag}`);
  }
  return value;
}

function serverOrigin(baseUrl) {
  let raw = String(baseUrl || '').trim();
  if (!/^https?:\/\//i.test(raw)) raw = `http://${raw}`;
  const parsed = new URL(raw);
  if (!parsed.port) parsed.port = '8080';
  parsed.pathname = '';
  parsed.search = '';
  parsed.hash = '';
  return parsed.toString().replace(/\/$/, '');
}

function loadEnvFile(filePath) {
  const env = {};
  const raw = fs.readFileSync(filePath, 'utf8');
  for (const line of raw.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const idx = trimmed.indexOf('=');
    if (idx <= 0) continue;
    env[trimmed.slice(0, idx)] = trimmed.slice(idx + 1);
  }
  return env;
}

function listScenarioDirs() {
  return fs.readdirSync(SCENARIO_ROOT)
    .map((name) => path.join(SCENARIO_ROOT, name))
    .filter((p) => fs.existsSync(path.join(p, 'scenario.yaml')));
}

function parseYamlScalar(raw) {
  const value = raw.trim();
  if (value === 'true') return true;
  if (value === 'false') return false;
  if (value === 'null') return null;
  if (/^-?\d+(\.\d+)?$/.test(value)) return Number(value);
  if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
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
      if (!trimmed || trimmed.startsWith('#')) continue;
      return raw;
    }
    return null;
  }

  for (let i = 0; i < lines.length; i += 1) {
    const raw = lines[i];
    const trimmed = raw.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const indent = raw.match(/^\s*/)[0].length;

    while (stack.length > 1 && stack[stack.length - 1].indent >= indent) {
      stack.pop();
    }
    const parent = stack[stack.length - 1].container;

    if (trimmed.startsWith('- ')) {
      if (!Array.isArray(parent)) {
        throw new Error(`invalid yaml list item near: ${trimmed}`);
      }
      parent.push(parseYamlScalar(trimmed.slice(2)));
      continue;
    }

    const idx = trimmed.indexOf(':');
    if (idx < 0) {
      throw new Error(`invalid yaml line: ${trimmed}`);
    }
    const key = trimmed.slice(0, idx).trim();
    const rest = trimmed.slice(idx + 1).trim();

    if (rest) {
      parent[key] = parseYamlScalar(rest);
      continue;
    }

    const nextRaw = nextMeaningfulLine(i + 1);
    const nextTrimmed = nextRaw ? nextRaw.trim() : '';
    const nextIndent = nextRaw ? nextRaw.match(/^\s*/)[0].length : -1;
    const container = nextTrimmed.startsWith('- ') && nextIndent > indent ? [] : {};
    parent[key] = container;
    stack.push({ indent, container });
  }

  return root;
}

function loadScenarioById(id) {
  for (const dir of listScenarioDirs()) {
    const yamlPath = path.join(dir, 'scenario.yaml');
    const data = parseSimpleYaml(fs.readFileSync(yamlPath, 'utf8'));
    if (data.id === id || path.basename(dir) === id) return { dir, data, yamlPath };
  }
  throw new Error(`scenario not found: ${id}`);
}

function nowStamp() {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
}

async function httpJson(url, init = {}) {
  const res = await fetch(url, init);
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch {}
  if (!res.ok) {
    throw new Error(`${res.status} ${res.statusText}: ${text}`);
  }
  return data;
}

async function uploadFile(apiOrigin, token, sessionId, filePath) {
  const form = new FormData();
  const buf = fs.readFileSync(filePath);
  form.append('file', new Blob([buf], { type: 'text/csv' }), path.basename(filePath));
  const res = await fetch(`${apiOrigin}/api/upload?session_id=${encodeURIComponent(sessionId)}`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    body: form,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch {}
  if (!res.ok) throw new Error(`upload failed for ${path.basename(filePath)}: ${res.status} ${text}`);
  return data;
}

function summarizeEvents(events) {
  const toolCalls = events.filter((e) => e.type === 'tool_call').map((e) => e.data?.name).filter(Boolean);
  const terminal = [...events].reverse().find((e) => ['run_completed', 'error', 'user_request_input', 'run_cancelled'].includes(e.type));
  const reportFinal = events.find((e) => e.type === 'report_final');
  const asked = events.find((e) => e.type === 'user_request_input');
  const err = events.find((e) => e.type === 'error');
  const errorMessage = err?.data?.message || '';
  return {
    event_count: events.length,
    tool_calls: toolCalls,
    unique_tool_calls: [...new Set(toolCalls)],
    terminal_type: terminal?.type || '',
    terminal_payload: terminal?.data || null,
    asked_user: Boolean(asked),
    user_question: asked?.data?.question || '',
    has_report_final: Boolean(reportFinal),
    error_message: errorMessage,
    error_category: classifyErrorMessage(errorMessage),
  };
}

function classifyErrorMessage(message) {
  const text = normalizeText(message);
  if (!text) return '';
  if (text.includes('insufficient balance') || text.includes('status=402')) return 'llm_request_failed';
  if (text.includes('tls handshake timeout')) return 'llm_network_timeout';
  if (text.includes('llm api request failed') || text.includes('openai chat completions api call failed')) return 'llm_request_failed';
  if (text.includes('context deadline exceeded')) return 'timeout';
  if (text.includes('connection refused')) return 'connection_refused';
  return 'runtime_error';
}

function isInfraErrorCategory(category) {
  return Boolean(category) && category !== 'runtime_error';
}

function normalizeText(input) {
  return String(input || '')
    .toLowerCase()
    .replace(/\s+/g, ' ')
    .trim();
}

function stripHtml(html) {
  return String(html || '')
    .replace(/<script[\s\S]*?<\/script>/gi, ' ')
    .replace(/<style[\s\S]*?<\/style>/gi, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/&nbsp;/gi, ' ')
    .replace(/&amp;/gi, '&')
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/\s+/g, ' ')
    .trim();
}

function safeJsonParse(raw) {
  if (typeof raw !== 'string') return null;
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

function pushText(target, value) {
  if (typeof value !== 'string') return;
  const trimmed = value.trim();
  if (trimmed) target.push(trimmed);
}

function clip(value, max = 240) {
  const text = String(value || '').replace(/\s+/g, ' ').trim();
  if (text.length <= max) return text;
  return `${text.slice(0, max - 1)}…`;
}

function extractResultPreview(raw) {
  const parsed = safeJsonParse(raw);
  if (!parsed || typeof parsed !== 'object') return clip(raw);
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
  const elapsed = `${String(record.elapsed_ms).padStart(7, ' ')}ms`;
  switch (event.type) {
    case 'run_started':
      return `${elapsed} run_started run_id=${data.runId || ''}`;
    case 'assistant_status':
      return `${elapsed} assistant_status ${clip(data.content)}`;
    case 'tool_call':
      return `${elapsed} tool_call ${data.name || ''} args=${clip(JSON.stringify(data.arguments || data.args || {}), 180)}`;
    case 'tool_result':
      return `${elapsed} tool_result ${data.name || ''} success=${data.success} duration_ms=${data.duration ?? ''} ${extractResultPreview(data.result)}`;
    case 'user_request_input':
      return `${elapsed} user_request_input ${clip(data.question || data.content || '')}`;
    case 'report_final':
      return `${elapsed} report_final title=${clip(data.title || '')}`;
    case 'run_completed':
      return `${elapsed} run_completed ${clip(data.summary)}`;
    case 'run_cancelled':
      return `${elapsed} run_cancelled ${clip(data.message || '')}`;
    case 'error':
      return `${elapsed} error ${clip(data.message || data.error || '')}`;
    default:
      return `${elapsed} ${event.type || 'event'} ${clip(JSON.stringify(data), 220)}`;
  }
}

function appendTrace(outDir, record) {
  fs.appendFileSync(path.join(outDir, 'events.ndjson'), `${JSON.stringify(record)}\n`);
  fs.appendFileSync(path.join(outDir, 'timeline.log'), `${formatTimelineLine(record)}\n`);
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
  if (!filePath || !fs.existsSync(filePath)) return '';
  const lines = fs.readFileSync(filePath, 'utf8').trimEnd().split(/\r?\n/);
  return lines.slice(-lineCount).join('\n');
}

function printTimelineTail(filePath, lineCount = 80) {
  const tail = readTail(filePath, lineCount);
  if (!tail) return;
  console.error('--- scenario timeline tail ---');
  console.error(tail);
  console.error('--- end scenario timeline ---');
}

function collectEvidence(events, runData, reportHtml) {
  const toolCalls = [];
  const sqlTexts = [];
  const textFragments = [];
  const schemaColumns = new Set();
  const toolResults = [];

  for (const event of events) {
    if (event.type === 'tool_call' && event.data?.name) {
      toolCalls.push(event.data.name);
      continue;
    }

    if (event.type === 'run_completed') {
      pushText(textFragments, event.data?.summary);
      continue;
    }

    if (event.type === 'user_request_input') {
      pushText(textFragments, event.data?.question);
      if (Array.isArray(event.data?.options)) {
        for (const option of event.data.options) pushText(textFragments, option);
      }
      continue;
    }

    if (event.type === 'error') {
      pushText(textFragments, event.data?.message);
      continue;
    }

    if (event.type === 'report_final') {
      pushText(textFragments, stripHtml(event.data?.html || ''));
      pushText(textFragments, event.data?.title);
      continue;
    }

    if (event.type !== 'tool_result') continue;

    const parsed = safeJsonParse(event.data?.result || '');
    if (!parsed || typeof parsed !== 'object') {
      pushText(textFragments, event.data?.result || '');
      continue;
    }

    toolResults.push({ name: event.data?.name || parsed.tool || '', payload: parsed });
    pushText(textFragments, parsed.ui_summary);
    pushText(textFragments, parsed.message);
    pushText(textFragments, parsed.result);
    pushText(textFragments, parsed.delegate_summary);
    pushText(textFragments, parsed.report_title);
    pushText(textFragments, parsed.author);

    if (typeof parsed.sql === 'string' && parsed.sql.trim()) {
      sqlTexts.push(parsed.sql);
    }

    if (Array.isArray(parsed.finalize_issues)) {
      pushText(textFragments, parsed.finalize_issues.join(' '));
    }

    if (parsed.schema && Array.isArray(parsed.schema.columns)) {
      for (const column of parsed.schema.columns) {
        if (!column || typeof column.name !== 'string') continue;
        schemaColumns.add(normalizeText(column.name));
      }
    }
  }

  const run = runData?.run || {};
  pushText(textFragments, run.summary);
  if (run.report?.title) pushText(textFragments, run.report.title);
  if (reportHtml) pushText(textFragments, stripHtml(reportHtml));

  return {
    toolCalls,
    uniqueToolCalls: [...new Set(toolCalls)],
    sqlTexts,
    sqlText: sqlTexts.join('\n\n'),
    text: textFragments.join('\n'),
    normalizedText: normalizeText(textFragments.join('\n')),
    schemaColumns,
    toolResults,
  };
}

function textMatches(evidence, patterns) {
  return patterns.some((pattern) => pattern.test(evidence.text));
}

function sqlMatches(evidence, patterns) {
  return evidence.sqlTexts.some((sql) => patterns.some((pattern) => pattern.test(sql)));
}

function hasColumn(evidence, columnName) {
  return evidence.schemaColumns.has(normalizeText(columnName));
}

function matchRequiredMention(label, evidence, summary) {
  const textPatterns = {
    trend: [/趋势/i, /\btrend\b/i, /月度/i, /按月/i, /走势/i],
    region: [/区域/i, /\bregion\b/i],
    channel: [/渠道/i, /\bchannel\b/i],
    product: [/产品/i, /品类/i, /\bproduct\b/i, /\bprod/i],
    orders: [/订单/i, /\borders?\b/i],
    capacity: [/产能/i, /产出/i, /\bcapacity\b/i, /\boutput\b/i],
    inventory: [/库存/i, /\binventory\b/i],
    quality: [/质量/i, /退货/i, /报废/i, /\bquality\b/i, /\breturn/i, /\bscrap\b/i],
    'missing region dimension': [/缺少.*区域/i, /没有.*region/i, /missing region/i, /无法.*区域/i],
    'cannot compute roi without revenue': [/无法.*roi/i, /cannot compute roi/i, /缺少.*收入/i, /没有.*收入/i],
    'no stable join key': [/没有共同键/i, /缺少稳定.*键/i, /no stable join key/i, /无法直接归因/i],
    'data quality issue': [/数据质量/i, /空值/i, /缺失/i, /异常/i, /负值/i, /quality issue/i],
    'confidence limit': [/可信度/i, /置信/i, /限制/i, /需谨慎/i, /confidence limit/i],
    'store comparison': [/门店/i, /\bstore\b/i, /\bshop\b/i],
    'mrr trend': [/\bmrr\b/i, /订阅收入/i, /经常性收入/i],
    'channel efficiency': [/渠道效率/i, /\broi\b/i, /\bcac\b/i, /获客效率/i],
    'multiple revenue definitions exist': [/多个.*收入/i, /multiple revenue/i, /口径/i, /gross_revenue/i, /net_revenue/i, /recognized_revenue/i],
    'no stable attribution key': [/没有共同键/i, /没有共同字段/i, /没有可直接关联的归因键/i, /没有直接.*join key/i, /缺少稳定.*归因/i, /no stable attribution key/i, /无法直接归因/i],
    'grain mismatch between campaign daily spend and monthly bookings': [/按日/i, /daily/i, /按月/i, /monthly/i, /粒度/i],
  };

  if (label === 'month + channel join') {
    return sqlMatches(evidence, [
      /\bjoin\b[\s\S]*\bon\b[\s\S]*\bmonth\b[\s\S]*\bchannel\b/i,
      /\bs\.\s*month\s*=\s*\w+\.\s*month[\s\S]*s\.\s*channel\s*=\s*\w+\.\s*channel/i,
    ]);
  }

  if (label === 'aggregated daily spend to monthly grain before comparison') {
    return textMatches(evidence, [/按月汇总/i, /按月把/i, /aggregat(ed)? .*monthly/i, /先.*按月/i, /汇总.*月/i, /对齐后分析/i]) || sqlMatches(evidence, [
      /substr\s*\(\s*dt\s*,\s*1\s*,\s*7\s*\)/i,
      /substr\s*\(\s*cast\s*\(\s*dt\s+as\s+text\s*\)\s*,\s*1\s*,\s*7\s*\)/i,
      /strftime\s*\(\s*['"]%Y-%m['"]\s*,\s*dt\s*\)/i,
    ]);
  }

  if (label === 'normalized revenue_10k_cny before comparing with cost_cny') {
    return sqlMatches(evidence, [
      /revenue_10k_cny\s*\*\s*10000/i,
      /gross_profit_10k_cny\s*\*\s*10000/i,
    ]);
  }

  if (label === 'date field mapped from dt') {
    return hasColumn(evidence, 'dt') && (
      sqlMatches(evidence, [/\bdt\b/i]) ||
      textMatches(evidence, [/趋势/i, /按日/i, /日期/i, /\bdate\b/i])
    );
  }

  if (label === 'revenue field mapped from gmv') {
    return hasColumn(evidence, 'gmv') && (
      sqlMatches(evidence, [/\bgmv\b/i]) ||
      textMatches(evidence, [/gmv/i, /销售额/i, /收入/i])
    );
  }

  if (label === 'channel mapped between 渠道 and channel_name') {
    return hasColumn(evidence, '渠道') &&
      hasColumn(evidence, 'channel_name') &&
      (
        sqlMatches(evidence, [
          /渠道\s*=\s*channel_name/i,
          /channel_name\s*=\s*渠道/i,
          /\bon\b[\s\S]*渠道[\s\S]*channel_name/i,
        ]) ||
        textMatches(evidence, [/渠道.*channel_name/i, /channel_name.*渠道/i])
      );
  }

  if (textPatterns[label]) {
    return textMatches(evidence, textPatterns[label]) || sqlMatches(evidence, textPatterns[label]);
  }

  const normalizedLabel = normalizeText(label);
  return evidence.normalizedText.includes(normalizedLabel) || normalizeText(evidence.sqlText).includes(normalizedLabel);
}

function matchForbiddenClaim(label, evidence, summary) {
  const textPatterns = {
    'missing critical fields': [/missing critical fields/i, /缺少关键字段/i],
    'tables cannot be combined': [/tables cannot be combined/i, /cannot be combined/i, /无法合并/i, /不能合并/i],
    'region comparison completed with confidence': [/区域.*对比/i, /各区域/i, /region comparison/i],
    'attributed revenue exists': [/attributed revenue/i, /归因收入/i],
    'all conclusions are highly reliable': [/highly reliable/i, /高度可靠/i, /非常可靠/i],
    'unnecessary uncertainty about available fields': [/无法识别字段/i, /字段不明确/i, /available fields.*uncertain/i],
    'exact attribution between pipeline and mrr without stating limits': [/精确归因/i, /直接归因/i, /exact attribution/i],
    'unsupported causal certainty': [/直接导致/i, /证明.*导致/i, /caused by/i],
    'cannot analyze due to missing standard headers': [/missing standard headers/i, /缺少标准字段名/i, /无法分析.*字段名/i],
    'channel fields are incompatible': [/channel fields are incompatible/i, /渠道字段.*不兼容/i],
    'exact roi attribution by campaign or region': [/精确.*roi.*归因/i, /exact roi attribution/i],
  };

  if (label === 'selected one revenue definition without clarification') {
    return !summary.asked_user && summary.terminal_type === 'run_completed' && (
      sqlMatches(evidence, [/\bgross_revenue\b/i, /\bnet_revenue\b/i, /\brecognized_revenue\b/i]) ||
      textMatches(evidence, [/gross_revenue/i, /net_revenue/i, /recognized_revenue/i])
    );
  }

  if (label === 'directly attributed crm bookings to campaign spend') {
    if (summary.terminal_type !== 'run_completed') {
      return false;
    }
    return sqlMatches(evidence, [
      /\bjoin\b[\s\S]*campaign[\s\S]*crm/i,
      /\bcampaign[_a-z]*\b[\s\S]*bookings/i,
      /\bcrm_bookings\b[\s\S]*campaign/i,
    ]) || textMatches(evidence, [/campaign.*bookings/i, /bookings.*campaign/i]);
  }

  if (label === 'compared daily spend directly against monthly revenue without aggregation') {
    const directJoin = sqlMatches(evidence, [
      /\bjoin\b[\s\S]*\bdt\b\s*=\s*\w+\.\s*month/i,
      /\bjoin\b[\s\S]*\bmonth\b\s*=\s*\w+\.\s*dt\b/i,
    ]);
    const hasAggregation = matchRequiredMention('aggregated daily spend to monthly grain before comparison', evidence, summary);
    return directJoin || (!hasAggregation && sqlMatches(evidence, [/\bjoin\b[\s\S]*\bdt\b[\s\S]*\bmonth\b/i]));
  }

  if (label === 'compared 10k cny revenue directly with cny cost without conversion') {
    return sqlMatches(evidence, [/\brevenue_10k_cny\b/i]) &&
      !matchRequiredMention('normalized revenue_10k_cny before comparing with cost_cny', evidence, summary);
  }

  if (textPatterns[label]) {
    return textMatches(evidence, textPatterns[label]) || sqlMatches(evidence, textPatterns[label]);
  }

  const normalizedLabel = normalizeText(label);
  return evidence.normalizedText.includes(normalizedLabel) || normalizeText(evidence.sqlText).includes(normalizedLabel);
}

function matchRequiredToolCall(name, evidence) {
  return evidence.uniqueToolCalls.includes(name);
}

function matchToolResultCode(spec, evidence) {
  const idx = String(spec || '').indexOf(':');
  if (idx <= 0) return false;
  const toolName = spec.slice(0, idx).trim();
  const errorCode = spec.slice(idx + 1).trim();
  if (!toolName || !errorCode) return false;
  return evidence.toolResults.some(({ name, payload }) => {
    const actualTool = String(name || payload?.tool || '').trim();
    const actualCode = String(payload?.error_code || '').trim();
    return actualTool === toolName && actualCode === errorCode;
  });
}

function evaluateAutoMap(expected, evidence, summary) {
  if (expected === true) {
    return !summary.asked_user && summary.terminal_type !== 'error';
  }
  if (expected === false) {
    return summary.terminal_type !== 'error';
  }
  return true;
}

function evaluateCoverage(expected, evidence, summary) {
  if (!expected) return true;
  if (expected === 'sufficient') {
    return summary.terminal_type === 'run_completed' && !summary.asked_user && !summary.error_message;
  }
  if (expected === 'partial') {
    return !summary.error_message && ['run_completed', 'user_request_input'].includes(summary.terminal_type);
  }
  return true;
}

function evaluateScenario(scenario, events, runData, reportHtml, summary) {
  const expected = scenario.data.expected || {};
  const evidence = collectEvidence(events, runData, reportHtml);
  const checks = [];

  function addCheck(name, pass, details) {
    checks.push({ name, pass, details });
  }

  if (typeof expected.should_inspect_schema === 'boolean') {
    addCheck(
      'should_inspect_schema',
      expected.should_inspect_schema ? evidence.uniqueToolCalls.includes('data_describe_table') : true,
      expected.should_inspect_schema
        ? `tool_calls=${evidence.uniqueToolCalls.join(', ')}`
        : 'not enforced',
    );
  }

  if (typeof expected.should_auto_map === 'boolean') {
    addCheck(
      'should_auto_map',
      evaluateAutoMap(expected.should_auto_map, evidence, summary),
      `expected=${expected.should_auto_map} asked_user=${summary.asked_user} terminal=${summary.terminal_type}`,
    );
  }

  if (typeof expected.should_ask_user === 'boolean') {
    addCheck(
      'should_ask_user',
      summary.asked_user === expected.should_ask_user,
      `expected=${expected.should_ask_user} actual=${summary.asked_user}`,
    );
  }

  if (typeof expected.should_finalize_report === 'boolean') {
    addCheck(
      'should_finalize_report',
      summary.has_report_final === expected.should_finalize_report,
      `expected=${expected.should_finalize_report} actual=${summary.has_report_final}`,
    );
  }

  if (typeof expected.expected_coverage === 'string') {
    addCheck(
      'expected_coverage',
      evaluateCoverage(expected.expected_coverage, evidence, summary),
      `expected=${expected.expected_coverage} terminal=${summary.terminal_type}`,
    );
  }

  for (const label of expected.required_mentions || []) {
    addCheck(
      `required:${label}`,
      matchRequiredMention(label, evidence, summary),
      label,
    );
  }

  for (const label of expected.forbidden_claims || []) {
    addCheck(
      `forbidden:${label}`,
      !matchForbiddenClaim(label, evidence, summary),
      label,
    );
  }

  for (const toolName of expected.required_tool_calls || []) {
    addCheck(
      `required_tool_call:${toolName}`,
      matchRequiredToolCall(toolName, evidence),
      toolName,
    );
  }

  for (const spec of expected.required_tool_result_codes || []) {
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
    blocked_by_infra: summary.terminal_type === 'error' && isInfraErrorCategory(summary.error_category),
    checks,
    failed_checks: failed,
  };
}

async function main() {
  const args = parseArgs(process.argv);
  const apiOrigin = serverOrigin(args.baseUrl);
  const wsOrigin = apiOrigin.replace(/^http/, 'ws');
  const env = loadEnvFile(path.join(ROOT, 'server', '.env'));
  const loginEmail = env.DEFAULT_USER_EMAIL;
  const loginPassword = env.DEFAULT_USER_PASSWORD;
  const workspaceId = env.DEFAULT_WORKSPACE_ID;
  if (!loginEmail || !loginPassword || !workspaceId) {
    throw new Error('missing default login credentials in server/.env');
  }

  const scenario = loadScenarioById(args.scenarioId);
  const outDir = path.join(OUTPUT_ROOT, `${nowStamp()}-${scenario.data.id}`);
  fs.mkdirSync(outDir, { recursive: true });
  activeTimelinePath = path.join(outDir, 'timeline.log');
  const startedAt = Date.now();
  appendTimelineNote(outDir, startedAt, 'scenario_started', `id=${scenario.data.id}`);

  const login = await httpJson(`${apiOrigin}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email: loginEmail, password: loginPassword, workspaceId }),
  });
  const token = login.token;
  if (!token) throw new Error('login returned no token');
  appendTimelineNote(outDir, startedAt, 'login_completed', `email=${loginEmail}`);

  const created = await httpJson(`${apiOrigin}/api/sessions`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
  });
  const sessionId = created?.session?.id;
  if (!sessionId) throw new Error('session creation failed');
  appendTimelineNote(outDir, startedAt, 'session_created', `session_id=${sessionId}`);

  const uploads = [];
  for (const rel of scenario.data.files || []) {
    const fullPath = path.join(scenario.dir, rel);
    uploads.push(await uploadFile(apiOrigin, token, sessionId, fullPath));
    appendTimelineNote(outDir, startedAt, 'file_uploaded', path.basename(fullPath));
  }

  const wsUrl = `${wsOrigin}/ws?token=${encodeURIComponent(token)}&session_id=${encodeURIComponent(sessionId)}`;
  const ws = new WebSocket(wsUrl);
  const events = [];
  let runId = '';
  let doneResolve;
  let doneReject;
  const done = new Promise((resolve, reject) => { doneResolve = resolve; doneReject = reject; });
  const timer = setTimeout(() => {
    appendTimelineNote(outDir, startedAt, 'scenario_timeout', `after ${args.timeoutSec}s`);
    doneReject(new Error(`scenario timeout after ${args.timeoutSec}s`));
  }, args.timeoutSec * 1000);

  ws.addEventListener('open', () => {
    ws.send(JSON.stringify({
      type: 'user_message',
      sessionId,
      runId: '',
      data: { content: scenario.data.prompt },
    }));
  });

  ws.addEventListener('message', (msg) => {
    const event = JSON.parse(msg.data.toString());
    events.push(event);
    appendTrace(outDir, {
      at: new Date().toISOString(),
      elapsed_ms: Date.now() - startedAt,
      event,
    });
    if (process.env.SCENARIO_TRACE === '1') {
      console.error(formatTimelineLine({ elapsed_ms: Date.now() - startedAt, event }));
    }
    if (event.type === 'run_started' && event.data?.runId) {
      runId = event.data.runId;
    }
    if (['run_completed', 'error', 'user_request_input', 'run_cancelled'].includes(event.type)) {
      doneResolve();
    }
  });

  ws.addEventListener('error', (err) => {
    appendTimelineNote(outDir, startedAt, 'websocket_error', err.message || 'unknown');
    doneReject(new Error(`websocket error: ${err.message || 'unknown'}`));
  });

  try {
    await done;
  } finally {
    clearTimeout(timer);
    try { ws.close(); } catch {}
  }

  let runData = null;
  let reportHtml = '';
  if (runId) {
    try {
      runData = await httpJson(`${apiOrigin}/api/runs/${encodeURIComponent(runId)}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch (err) {
      runData = { fetch_error: String(err) };
    }
    try {
      const res = await fetch(`${apiOrigin}/api/runs/${encodeURIComponent(runId)}/report`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (res.ok) reportHtml = await res.text();
    } catch {}
  }

  const summary = summarizeEvents(events);
  const evaluation = evaluateScenario(scenario, events, runData, reportHtml, summary);
  const result = {
    scenario_id: scenario.data.id,
    scenario_dir: path.relative(ROOT, scenario.dir),
    prompt: scenario.data.prompt,
    files: scenario.data.files,
    expected: scenario.data.expected || {},
    session_id: sessionId,
    run_id: runId,
    uploads,
    summary,
    evaluation,
  };

  fs.writeFileSync(path.join(outDir, 'summary.json'), JSON.stringify(result, null, 2));
  fs.writeFileSync(path.join(outDir, 'events.json'), JSON.stringify(events, null, 2));
  if (runData) fs.writeFileSync(path.join(outDir, 'run.json'), JSON.stringify(runData, null, 2));
  if (reportHtml) fs.writeFileSync(path.join(outDir, 'report.html'), reportHtml);

  console.log(JSON.stringify({
    out_dir: path.relative(ROOT, outDir),
    timeline: path.relative(ROOT, activeTimelinePath),
    ...summary,
    run_id: runId,
    evaluation: {
      pass: evaluation.pass,
      failed_count: evaluation.failed_count,
      failed_checks: evaluation.failed_checks.map((item) => item.name),
    },
  }, null, 2));

  if (!evaluation.pass) {
    printTimelineTail(activeTimelinePath);
    process.exit(2);
  }
}

main().catch((err) => {
  printTimelineTail(activeTimelinePath);
  const message = err && err.message ? err.message : String(err);
  if (!activeTimelinePath) {
    console.error(`Error: ${message}`);
  } else {
    console.error(err.stack || `Error: ${message}`);
  }
  process.exit(1);
});
