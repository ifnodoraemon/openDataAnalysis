(function () {
  function showMessage(el, message, color) {
    while (el.firstChild) {
      el.removeChild(el.firstChild);
    }

    var box = document.createElement("div");
    box.style.display = "flex";
    box.style.alignItems = "center";
    box.style.justifyContent = "center";
    box.style.height = "100%";
    box.style.color = color;
    box.style.fontSize = "14px";
    box.textContent = String(message || "");
    el.appendChild(box);
  }

  function parseOption(el) {
    var raw = el.getAttribute("data-chart-option") || "";
    if (!raw.trim()) return {};
    return JSON.parse(raw);
  }

  function firstComponent(component) {
    return Array.isArray(component) ? component[0] : component;
  }

  function ensureObject(value) {
    return value && typeof value === "object" && !Array.isArray(value)
      ? value
      : {};
  }

  function normalizeChartLayout(option) {
    if (!option || typeof option !== "object") return option;

    var title = firstComponent(option.title);
    var legend = firstComponent(option.legend);
    var hasTitle = title && typeof title === "object" && title.text;
    var hasLegend = legend && typeof legend === "object";

    if (hasTitle) {
      title.left = title.left || "center";
      title.top = title.top == null ? 8 : title.top;
      title.textStyle = Object.assign(
        { fontSize: 18, fontWeight: 700, color: "#374151" },
        ensureObject(title.textStyle),
      );
    }

    if (hasLegend) {
      legend.left = legend.left || "center";
      legend.top = legend.top == null ? (hasTitle ? 42 : 12) : legend.top;
      legend.type = legend.type || "scroll";
      legend.itemGap = legend.itemGap || 18;
    }

    var hasAxis = option.xAxis || option.yAxis;
    if (hasAxis) {
      if (!option.grid) option.grid = {};
      var grids = Array.isArray(option.grid) ? option.grid : [option.grid];
      grids.forEach(function (grid) {
        if (!grid || typeof grid !== "object") return;
        grid.left = grid.left || "7%";
        grid.right = grid.right || "7%";
        grid.bottom = grid.bottom || "8%";
        grid.top =
          grid.top == null ? (hasTitle || hasLegend ? 88 : 36) : grid.top;
        if (grid.containLabel == null) grid.containLabel = true;
      });
    }

    return option;
  }

  function renderCharts() {
    var nodes = document.querySelectorAll(".chart-box[data-chart-id]");
    nodes.forEach(function (el) {
      try {
        if (!window.echarts || typeof window.echarts.init !== "function") {
          showMessage(
            el,
            "Chart render failed: ECharts library is not loaded",
            "#e53e3e",
          );
          return;
        }
        var option = parseOption(el);
        if (
          !option ||
          typeof option !== "object" ||
          Object.keys(option).length === 0
        ) {
          showMessage(el, "Chart data is empty", "#999");
          return;
        }
        if (!option.tooltip) option.tooltip = { trigger: "axis" };
        normalizeChartLayout(option);
        var chart = window.echarts.init(el);
        chart.setOption(option);
        window.addEventListener("resize", function () {
          chart.resize();
        });
      } catch (error) {
        showMessage(el, "Chart render failed: " + error.message, "#e53e3e");
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", renderCharts);
  } else {
    renderCharts();
  }
})();
