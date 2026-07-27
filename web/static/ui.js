// Shared dashboard UI behaviour, loaded on every page from layout.html.
//
// Everything here is driven by data-attributes and bound with delegated
// listeners on document, for two reasons:
//
//  1. CSP. The Content-Security-Policy sets script-src 'self' with no
//     'unsafe-inline', which blocks inline <script> blocks *and* inline
//     on*= handlers. Behaviour therefore has to live in a served file.
//  2. htmx. Delegation from document means swapped-in fragments work
//     immediately, with no re-initialisation hook after each swap.
//
// Supported attributes:
//   data-show="<id>"                 remove .hidden from #<id>
//   data-hide="<id>"                 add .hidden to #<id>
//   data-toggle="<id>"               toggle .hidden on #<id>
//   data-tab-group="<g>" data-tab="<name>"
//                                    activate #<g>-panel-<name>, and style
//                                    #<g>-btn-<name> as the selected tab
(function () {
  "use strict";

  // Utility classes that mark a tab button selected. Kept as the exact class
  // lists the templates already use so the rendering is unchanged; only these
  // toggle, while each button's size/shape classes stay in the markup.
  var TAB_ACTIVE = ["text-white", "bg-indigo-950/60", "border-indigo-500"];
  var TAB_INACTIVE = ["border-transparent", "text-gray-400", "hover:text-gray-200"];

  function byId(id) {
    return id ? document.getElementById(id) : null;
  }

  // switchTab shows one panel in a group and hides its siblings. Panels and
  // buttons are discovered by convention: #<group>-panel-<name> / #<group>-btn-<name>.
  // display is set inline with !important because the panels' base class list
  // includes `hidden`, which would otherwise win.
  function switchTab(group, name) {
    var buttons = document.querySelectorAll('[data-tab-group="' + group + '"]');
    Array.prototype.forEach.call(buttons, function (btn) {
      var tab = btn.getAttribute("data-tab");
      var panel = byId(group + "-panel-" + tab);
      var selected = tab === name;

      if (panel) {
        panel.style.setProperty("display", selected ? "block" : "none", "important");
      }
      btn.classList.remove.apply(btn.classList, selected ? TAB_INACTIVE : TAB_ACTIVE);
      btn.classList.add.apply(btn.classList, selected ? TAB_ACTIVE : TAB_INACTIVE);
      btn.setAttribute("aria-selected", selected ? "true" : "false");
    });
  }

  // Select each group's first tab, unless one is already marked selected.
  function initTabs() {
    var seen = {};
    var buttons = document.querySelectorAll("[data-tab-group]");
    Array.prototype.forEach.call(buttons, function (btn) {
      var group = btn.getAttribute("data-tab-group");
      if (seen[group]) {
        return;
      }
      seen[group] = true;
      var current = document.querySelector(
        '[data-tab-group="' + group + '"][aria-selected="true"]'
      );
      switchTab(group, (current || btn).getAttribute("data-tab"));
    });
  }

  document.addEventListener("click", function (event) {
    var el = event.target.closest ? event.target.closest("[data-show],[data-hide],[data-toggle],[data-tab]") : null;
    if (!el) {
      return;
    }

    var show = byId(el.getAttribute("data-show"));
    if (show) {
      show.classList.remove("hidden");
    }
    var hide = byId(el.getAttribute("data-hide"));
    if (hide) {
      hide.classList.add("hidden");
    }
    var toggle = byId(el.getAttribute("data-toggle"));
    if (toggle) {
      toggle.classList.toggle("hidden");
    }

    var group = el.getAttribute("data-tab-group");
    if (group) {
      switchTab(group, el.getAttribute("data-tab"));
    }
  });

  document.addEventListener("DOMContentLoaded", initTabs);
  // Re-assert tab state after an htmx swap, in case the swap replaced a panel.
  document.body && document.body.addEventListener("htmx:afterSwap", initTabs);
})();
