#!/usr/bin/env node
/**
 * eval_report.js — 批量评估 scenario 跑批结果
 *
 * 递归扫描 tmp/scenario-runs/ 下所有 summary.json，
 * 汇总 pass/fail 情况并输出 Markdown 统计表。
 * 若存在 pass=false 的场景，以退出码 2 退出。
 *
 * 用法：
 *   node scripts/eval_report.js [--results-dir tmp/scenario-runs] [--help]
 */

'use strict';

const fs = require('fs');
const path = require('path');

const ROOT = process.cwd();

function parseArgs(argv) {
  const args = {
    resultsDir: path.join(ROOT, 'tmp', 'scenario-runs'),
    help: false,
  };
  for (let i = 2; i < argv.length; i++) {
    if (argv[i] === '--results-dir') args.resultsDir = argv[++i];
    else if (argv[i] === '--help' || argv[i] === '-h') args.help = true;
  }
  return args;
}

function findSummaryFiles(dir) {
  const results = [];
  if (!fs.existsSync(dir)) return results;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      results.push(...findSummaryFiles(fullPath));
    } else if (entry.isFile() && entry.name === 'summary.json') {
      results.push(fullPath);
    }
  }
  return results;
}

function loadSummary(filePath) {
  try {
    return JSON.parse(fs.readFileSync(filePath, 'utf8'));
  } catch {
    return null;
  }
}

function formatTable(rows, headers) {
  const widths = headers.map((h, i) =>
    Math.max(h.length, ...rows.map((r) => String(r[i] ?? '').length))
  );
  const sep = widths.map((w) => '-'.repeat(w)).join(' | ');
  const header = headers.map((h, i) => h.padEnd(widths[i])).join(' | ');
  const lines = [header, sep];
  for (const row of rows) {
    lines.push(row.map((cell, i) => String(cell ?? '').padEnd(widths[i])).join(' | '));
  }
  return lines.join('\n');
}

function main() {
  const args = parseArgs(process.argv);
  const skipInfraFailures = process.env.SKIP_INFRA_FAILURES === '1';
  if (args.help) {
    console.log('Usage: node scripts/eval_report.js [--results-dir tmp/scenario-runs]');
    console.log('');
    console.log('Reads all summary.json files under the results directory and');
    console.log('prints a Markdown pass/fail report. Exits with code 2 if any');
    console.log('scenario failed.');
    process.exit(0);
  }

  const summaryFiles = findSummaryFiles(args.resultsDir);
  if (summaryFiles.length === 0) {
    console.log(`# Eval Report\n\n无可用场景结果（${path.relative(ROOT, args.resultsDir)}）`);
    process.exit(0);
  }

  const records = [];
  for (const file of summaryFiles.sort()) {
    const data = loadSummary(file);
    if (!data) continue;

    const eval_ = data.evaluation || {};
    const summary = data.summary || {};
    const failedChecks = (eval_.failed_checks || []).map((c) => c.name || c).join(', ');
    const toolCallCount = (summary.tool_calls || []).length;
    const errorCategory = summary.error_category || '';

    records.push({
      scenarioId: data.scenario_id || path.basename(path.dirname(file)),
      pass: eval_.pass === true,
      blockedByInfra: eval_.blocked_by_infra === true,
      failedCount: eval_.failed_count || 0,
      failedChecks,
      terminal: summary.terminal_type || '',
      toolCalls: toolCallCount,
      errorCategory,
      runDir: path.relative(ROOT, path.dirname(file)),
    });
  }

  const passCount = records.filter((r) => r.pass).length;
  const infraBlockedCount = records.filter((r) => !r.pass && r.blockedByInfra).length;
  const failCount = records.filter((r) => !r.pass && !(skipInfraFailures && r.blockedByInfra)).length;
  const total = records.length;
  const passRate = total > 0 ? Math.round((passCount / total) * 100) : 0;

  const lines = [];
  lines.push('# Eval Report');
  lines.push('');
  lines.push(`**总计**: ${total} 场景 | **通过**: ${passCount} (${passRate}%) | **失败**: ${failCount} | **基础设施阻断**: ${infraBlockedCount}`);
  lines.push('');

  // 汇总表
  const tableRows = records.map((r) => [
    r.pass ? '✅' : (r.blockedByInfra ? '⚠️' : '❌'),
    r.scenarioId,
    String(r.toolCalls),
    r.terminal,
    r.errorCategory || '-',
    r.failedChecks || '-',
  ]);
  lines.push('## 场景结果');
  lines.push('');
  lines.push(formatTable(tableRows, ['状态', '场景 ID', '工具调用数', '终态', '错误类型', '失败检查项']));
  lines.push('');

  // 失败明细
  const failed = records.filter((r) => !r.pass);
  if (failed.length > 0) {
    lines.push('## 未通过明细');
    lines.push('');
    for (const r of failed) {
      lines.push(`### ${r.blockedByInfra ? '⚠️' : '❌'} ${r.scenarioId}`);
      lines.push(`- 目录: \`${r.runDir}\``);
      if (r.blockedByInfra) lines.push('- 分类: 基础设施阻断');
      lines.push(`- 终态: ${r.terminal}  错误类型: ${r.errorCategory || '-'}`);
      lines.push(`- 失败检查项 (${r.failedCount}):`);
      for (const check of (r.failedChecks || '-').split(', ').filter(Boolean)) {
        lines.push(`  - ${check}`);
      }
      lines.push('');
    }
  }

  // 工具调用频次统计
  const toolFreq = {};
  for (const file of summaryFiles) {
    const data = loadSummary(file);
    if (!data) continue;
    for (const tool of (data.summary?.tool_calls || [])) {
      toolFreq[tool] = (toolFreq[tool] || 0) + 1;
    }
  }
  const sortedTools = Object.entries(toolFreq).sort((a, b) => b[1] - a[1]).slice(0, 15);
  if (sortedTools.length > 0) {
    lines.push('## 工具调用频次 (Top 15)');
    lines.push('');
    lines.push(formatTable(sortedTools.map(([tool, count]) => [tool, String(count)]), ['工具名', '调用次数']));
    lines.push('');
  }

  const report = lines.join('\n');
  console.log(report);

  // 写 JSON 出口供 CI artifact 和后续脚本消费
  const jsonOut = path.join(args.resultsDir, '..', 'eval-report.json');
  try {
    fs.writeFileSync(jsonOut, JSON.stringify({
      generated_at: new Date().toISOString(),
      total,
      pass_count: passCount,
      fail_count: failCount,
      infra_blocked_count: infraBlockedCount,
      pass_rate: passRate,
      scenarios: records,
      tool_frequency: Object.fromEntries(sortedTools),
    }, null, 2));
    process.stderr.write(`eval-report.json => ${path.relative(ROOT, jsonOut)}\n`);
  } catch (err) {
    process.stderr.write(`warn: failed to write eval-report.json: ${err.message}\n`);
  }

  if (failCount > 0) {
    process.exitCode = 2;
  }
}

main();
