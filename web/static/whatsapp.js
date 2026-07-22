(function() {
  "use strict";

  window.switchWaTab = function(tabName) {
    var tabs = ["connection", "contacts", "messages"];
    tabs.forEach(function(t) {
      var panel = document.getElementById("wa-panel-" + t);
      var btn = document.getElementById("wa-btn-" + t);
      if (panel) {
        if (t === tabName) {
          panel.style.setProperty("display", "block", "important");
        } else {
          panel.style.setProperty("display", "none", "important");
        }
      }
      if (btn) {
        if (t === tabName) {
          btn.className = "wa-tab-btn px-4 py-2.5 text-sm font-semibold rounded-t-xl transition duration-200 border-b-2 text-white bg-indigo-950/60 border-indigo-500";
        } else {
          btn.className = "wa-tab-btn px-4 py-2.5 text-sm font-semibold rounded-t-xl transition duration-200 border-b-2 border-transparent text-gray-400 hover:text-gray-200";
        }
      }
    });
  };

  window.toggleWaEditDeck = function(chatID) {
    var deck = document.getElementById("wa-edit-deck-" + chatID);
    if (deck) {
      deck.classList.toggle("hidden");
    }
  };

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
