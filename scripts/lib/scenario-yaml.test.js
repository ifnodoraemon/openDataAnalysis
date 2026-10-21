// Unit tests for the scenario YAML reader.
//
// These exist because a silent parsing gap made every scenario with
// `terminal_types: [a, b]` unevaluable: the naive reader returned the bracket
// expression as a string, and the evaluation code then threw on `.join`. The
// scenario corpus itself is asserted too, so a scenario file that the reader
// cannot represent fails here rather than during a real run.

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const { parseYamlScalar, parseSimpleYaml } = require("./scenario-yaml");

const SCENARIO_ROOT = path.join(__dirname, "..", "..", "samples", "coverage_scenarios");

test("parses scalars", () => {
  assert.strictEqual(parseYamlScalar("true"), true);
  assert.strictEqual(parseYamlScalar("false"), false);
  assert.strictEqual(parseYamlScalar("null"), null);
  assert.strictEqual(parseYamlScalar("42"), 42);
  assert.strictEqual(parseYamlScalar("1.5"), 1.5);
  assert.strictEqual(parseYamlScalar('"quoted"'), "quoted");
  assert.strictEqual(parseYamlScalar("plain text"), "plain text");
});

test("parses flow sequences into arrays", () => {
  assert.deepStrictEqual(parseYamlScalar("[a, b]"), ["a", "b"]);
  assert.deepStrictEqual(parseYamlScalar("[user_request_input]"), [
    "user_request_input",
  ]);
  assert.deepStrictEqual(parseYamlScalar("[]"), []);
  assert.deepStrictEqual(parseYamlScalar("[1, true, x]"), [1, true, "x"]);
});

test("parses nested maps and block lists", () => {
  const parsed = parseSimpleYaml(
    [
      "id: sample",
      "files:",
      "  - a.csv",
      "  - b.csv",
      "acceptance:",
      "  terminal_types: [run_completed, user_request_input]",
      "  no_error: true",
      "  resume_if_asked: true",
      "manual_review:",
      "  - 第一条",
      "  - 第二条",
    ].join("\n"),
  );

  assert.strictEqual(parsed.id, "sample");
  assert.deepStrictEqual(parsed.files, ["a.csv", "b.csv"]);
  assert.deepStrictEqual(parsed.acceptance.terminal_types, [
    "run_completed",
    "user_request_input",
  ]);
  assert.strictEqual(parsed.acceptance.no_error, true);
  assert.strictEqual(parsed.acceptance.resume_if_asked, true);
  assert.deepStrictEqual(parsed.manual_review, ["第一条", "第二条"]);
});

test("ignores comments and blank lines", () => {
  const parsed = parseSimpleYaml("# leading comment\n\nid: x\n\n# trailing\n");
  assert.deepStrictEqual(parsed, { id: "x" });
});

// A scenario directory that holds data but no manifest is silently skipped by
// the runner, which means part of the corpus is never evaluated. This asserts
// the corpus has no such holes.
test("every data directory has a scenario manifest", () => {
  const missing = fs
    .readdirSync(SCENARIO_ROOT, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => path.join(SCENARIO_ROOT, entry.name))
    .filter((dir) =>
      fs.readdirSync(dir).some((name) => name.endsWith(".csv")),
    )
    .filter((dir) => !fs.existsSync(path.join(dir, "scenario.yaml")))
    .map((dir) => path.basename(dir));

  assert.deepStrictEqual(missing, [], `scenario directories without a manifest: ${missing.join(", ")}`);
});

// The whole corpus must parse, and every array-shaped acceptance field must
// actually be an array. This is the regression that made the benchmark unable
// to produce a result.
test("every scenario file parses into evaluable values", () => {
  const dirs = fs
    .readdirSync(SCENARIO_ROOT, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => path.join(SCENARIO_ROOT, entry.name));

  assert.ok(dirs.length > 0, "expected scenario directories");

  for (const dir of dirs) {
    const yamlPath = path.join(dir, "scenario.yaml");
    if (!fs.existsSync(yamlPath)) continue;
    const data = parseSimpleYaml(fs.readFileSync(yamlPath, "utf8"));
    assert.strictEqual(
      typeof data.id,
      "string",
      `${yamlPath}: id must be a string`,
    );

    const acceptance = data.acceptance || {};
    for (const key of ["terminal_types", "required_tool_calls", "required_tool_result_codes"]) {
      if (acceptance[key] === undefined) continue;
      assert.ok(
        Array.isArray(acceptance[key]),
        `${yamlPath}: acceptance.${key} must parse as an array, got ${typeof acceptance[key]}`,
      );
    }
    if (data.files !== undefined) {
      assert.ok(
        Array.isArray(data.files),
        `${yamlPath}: files must parse as an array`,
      );
    }
    if (data.answers !== undefined) {
      assert.ok(
        Array.isArray(data.answers),
        `${yamlPath}: answers must parse as an array`,
      );
    }
  }
});
