(function() {
  "use strict";

  function scrollMessagesToBottom() {
    const list = document.getElementById("wa-message-list");
    if (list) {
      list.scrollTop = list.scrollHeight;
    }
  }

  function initAll() {
    scrollMessagesToBottom();
  }


  // Clear the composer after a successful send. This replaces an
  // hx-on::after-request attribute: htmx implements hx-on via new Function(),
  // which a CSP without 'unsafe-eval' blocks.
  document.addEventListener("htmx:afterRequest", function (event) {
    var form = event.target;
    if (form && form.matches && form.matches("form[data-reset-on-success]")) {
      var detail = event.detail || {};
      var xhr = detail.xhr || {};
      if (detail.successful !== false && xhr.status >= 200 && xhr.status < 300) {
        form.reset();
      }
    }
  });

  document.addEventListener("DOMContentLoaded", function () {
    initAll();
  });

  document.body.addEventListener("htmx:afterSwap", function () {
    initAll();
  });
})();
