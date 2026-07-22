// workflow.js — CSP-clean list editors for the workflows control panel.
//
// Each element with class "js-list-editor" manages a repeatable list of rows
// (Skills manifest, MCP server registry) and mirrors the current rows into a
// hidden <input> as JSON on every change, so HTMX submits it like any field.
//
// Data attributes on the container:
//   data-field   name of the hidden input (e.g. "skills" | "mcp_servers")
//   data-init    JSON array of existing rows
//   data-text    comma list of "key:Label" text columns
//   data-bool    optional "key:Label" boolean (checkbox) column
(function () {
  "use strict";

  function parseCols(spec) {
    if (!spec) return [];
    return spec.split(",").map(function (pair) {
      var i = pair.indexOf(":");
      return { key: pair.slice(0, i), label: pair.slice(i + 1) };
    });
  }

  function el(tag, attrs, children) {
    var node = document.createElement(tag);
    Object.keys(attrs || {}).forEach(function (k) {
      if (k === "class") node.className = attrs[k];
      else if (k === "checked") node.checked = !!attrs[k];
      else node.setAttribute(k, attrs[k]);
    });
    (children || []).forEach(function (c) {
      node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    });
    return node;
  }

  var INPUT_CLS =
    "w-full bg-[#03060c]/60 border border-indigo-950/80 focus:border-indigo-500 focus:ring-1 focus:ring-indigo-500 rounded-lg px-3 py-2 text-xs text-slate-300 outline-none transition";

  function initEditor(container) {
    var field = container.getAttribute("data-field");
    var textCols = parseCols(container.getAttribute("data-text"));
    var boolCols = parseCols(container.getAttribute("data-bool"));
    var rows;
    try {
      rows = JSON.parse(container.getAttribute("data-init") || "[]");
    } catch (e) {
      rows = [];
    }
    if (!Array.isArray(rows)) rows = [];

    var hidden = el("input", { type: "hidden", name: field });
    var list = el("div", { class: "space-y-2" });
    container.appendChild(list);

    function sync() {
      hidden.value = JSON.stringify(rows);
    }

    function render() {
      list.innerHTML = "";
      rows.forEach(function (row, idx) {
        var cells = [];
        textCols.forEach(function (col) {
          var input = el("input", {
            type: "text",
            class: INPUT_CLS,
            placeholder: col.label,
            value: row[col.key] == null ? "" : String(row[col.key]),
          });
          input.addEventListener("input", function () {
            rows[idx][col.key] = input.value;
            sync();
          });
          cells.push(el("div", { class: "flex-1" }, [input]));
        });
        boolCols.forEach(function (col) {
          var cb = el("input", {
            type: "checkbox",
            class: "h-4 w-4 accent-indigo-500",
            checked: !!row[col.key],
          });
          cb.addEventListener("change", function () {
            rows[idx][col.key] = cb.checked;
            sync();
          });
          cells.push(
            el("label", { class: "flex items-center gap-1 text-xs text-gray-400" }, [cb, col.label])
          );
        });
        var remove = el(
          "button",
          {
            type: "button",
            class:
              "shrink-0 px-2 py-1 text-xs text-red-300 hover:text-red-200 rounded-lg border border-red-900/60 hover:bg-red-950/40 transition",
          },
          ["Remove"]
        );
        remove.addEventListener("click", function () {
          rows.splice(idx, 1);
          sync();
          render();
        });
        cells.push(remove);
        list.appendChild(el("div", { class: "flex items-center gap-2" }, cells));
      });
      if (rows.length === 0) {
        list.appendChild(
          el("p", { class: "text-xs text-gray-600 italic" }, ["No entries yet."])
        );
      }
    }

    var add = el(
      "button",
      {
        type: "button",
        class:
          "mt-2 px-3 py-1.5 text-xs font-semibold text-indigo-300 hover:text-indigo-200 rounded-lg border border-indigo-900/60 hover:bg-indigo-950/40 transition",
      },
      ["+ Add"]
    );
    add.addEventListener("click", function () {
      var blank = {};
      textCols.forEach(function (c) {
        blank[c.key] = "";
      });
      boolCols.forEach(function (c) {
        blank[c.key] = true;
      });
      rows.push(blank);
      sync();
      render();
    });

    container.appendChild(hidden);
    container.appendChild(add);
    sync();
    render();
  }

  function initAll(root) {
    (root || document).querySelectorAll(".js-list-editor").forEach(function (c) {
      if (!c.dataset.editorReady) {
        c.dataset.editorReady = "1";
        initEditor(c);
      }
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    initAll(document);
    initSSEStream();
  });
  // Re-init any editors that arrive via HTMX fragment swaps.
  document.body.addEventListener("htmx:afterSwap", function (e) {
    initAll(e.target);
  });

  function initSSEStream() {
    var streamContainer = document.getElementById("wf-log-stream");
    if (!streamContainer) return;
    try {
      var evtSource = new EventSource("/api/logs");
      evtSource.onmessage = function (e) {
        if (!e.data) return;
        var line = document.createElement("div");
        line.className = "py-0.5 border-b border-indigo-950/20 hover:bg-slate-900/60 text-slate-300";
        line.textContent = e.data;
        streamContainer.appendChild(line);
        if (streamContainer.children.length > 200) {
          streamContainer.removeChild(streamContainer.firstChild);
        }
        streamContainer.scrollTop = streamContainer.scrollHeight;
      };
    } catch (err) {
      console.warn("SSE connection to /api/logs unavailable", err);
    }
  }
})();
