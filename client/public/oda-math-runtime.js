(function () {
  function renderMath() {
    if (typeof renderMathInElement === "function") {
      renderMathInElement(document.body, {
        delimiters: [
          {left: '$$', right: '$$', display: true},
          {left: '\\(', right: '\\)', display: false},
          {left: '\\[', right: '\\]', display: true}
        ],
        throwOnError: false
      });
    }
  }

  // Use setInterval to wait for renderMathInElement just in case scripts load out of order
  // since auto-render.min.js is also deferred.
  var retries = 0;
  function tryRender() {
    if (typeof renderMathInElement === "function") {
      renderMath();
    } else if (retries < 50) {
      retries++;
      setTimeout(tryRender, 100);
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", tryRender);
  } else {
    tryRender();
  }
})();
