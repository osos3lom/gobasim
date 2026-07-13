(function() {
  "use strict";

  function scrollMessagesToBottom() {
    const list = document.getElementById("wa-message-list");
    if (list) {
      list.scrollTop = list.scrollHeight;
    }
  }

  function initAll(root) {
    scrollMessagesToBottom();
  }

  document.addEventListener("DOMContentLoaded", function () {
    initAll(document);
  });

  document.body.addEventListener("htmx:afterSwap", function (e) {
    initAll(e.target);
  });
})();
