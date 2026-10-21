// Minimal YAML reader for scenario files.
//
// Scenario YAML uses a small subset: scalars, block lists, nested maps, and
// flow sequences such as `terminal_types: [run_completed, user_request_input]`.
// It lives in its own module so the parsing rules are unit-testable without a
// server or a model — a silent parsing change previously made every scenario
// unevaluable.

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
  // Flow sequences such as `terminal_types: [run_completed, user_request_input]`
  // are valid YAML and are used throughout the scenario corpus. Without this
  // the value stays a string and every array-shaped acceptance field breaks at
  // evaluation time.
  if (value.startsWith("[") && value.endsWith("]")) {
    const inner = value.slice(1, -1).trim();
    if (!inner) return [];
    return inner.split(",").map((item) => parseYamlScalar(item));
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

module.exports = { parseYamlScalar, parseSimpleYaml };
