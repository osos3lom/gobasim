// Real-time log viewer for /dashboard/logs: SSE stream, severity
// classification, search/filter, autoscroll and export.
//
// Extracted from an inline <script> in logs.html so the CSP can drop
// 'unsafe-inline' — see securityHeaders in web/server.go.
(function() {
    const logWindow = document.getElementById('log-window');
    const clearBtn = document.getElementById('clear-btn');
    const exportBtn = document.getElementById('export-btn');
    const autoscrollBtn = document.getElementById('autoscroll-btn');
    const searchInput = document.getElementById('log-search');
    const filterBtns = document.querySelectorAll('.log-filter-btn');
    const sseIndicator = document.getElementById('sse-indicator');
    const MAX_LINES = 300;

    let autoScroll = true;
    let activeFilter = 'all';
    let searchTerm = '';
    let rawLines = [];

    // Severity classification
    function classifyLine(text) {
        const upper = text.toUpperCase();
        if (upper.includes('ERROR') || upper.includes('PANIC') || upper.includes('FATAL')) return 'error';
        if (upper.includes('WARN')) return 'warn';
        if (upper.includes('LLM') || upper.includes('NIM') || upper.includes('OPENAI') || upper.includes('GROQ') || upper.includes('COMPLETION')) return 'llm';
        if (upper.includes('ERP') || upper.includes('TOOL') || upper.includes('MSHALIA')) return 'erp';
        if (upper.includes('WHATSAPP') || upper.includes('WHATSMEOW') || upper.includes('WA ') || upper.includes('[WA]')) return 'wa';
        return 'info';
    }

    // Severity color classes
    function severityClasses(sev) {
        switch (sev) {
            case 'error': return 'text-red-400 bg-red-950/20';
            case 'warn':  return 'text-amber-400';
            case 'llm':   return 'text-purple-400';
            case 'erp':   return 'text-emerald-400';
            case 'wa':    return 'text-indigo-400';
            default:      return 'text-slate-400';
        }
    }

    function shouldShow(line) {
        if (activeFilter !== 'all' && line.sev !== activeFilter) return false;
        if (searchTerm && !line.text.toLowerCase().includes(searchTerm)) return false;
        return true;
    }

    function renderLines() {
        logWindow.innerHTML = '';
        rawLines.forEach(function(line) {
            if (!shouldShow(line)) return;
            var el = document.createElement('div');
            el.className = 'py-0.5 border-b border-indigo-950/15 leading-relaxed text-[11px] ' + severityClasses(line.sev);
            var arrow = document.createElement('span');
            arrow.className = 'text-indigo-600 select-none mr-1';
            arrow.textContent = '>';
            var text = document.createElement('span');
            text.textContent = line.text;
            el.appendChild(arrow);
            el.appendChild(text);
            logWindow.appendChild(el);
        });
        if (autoScroll) logWindow.scrollTop = logWindow.scrollHeight;
    }

    function addLine(text) {
        var sev = classifyLine(text);
        rawLines.push({ text: text, sev: sev });
        if (rawLines.length > MAX_LINES) rawLines.shift();

        if (!shouldShow({ text: text, sev: sev })) return;

        var el = document.createElement('div');
        el.className = 'py-0.5 border-b border-indigo-950/15 leading-relaxed text-[11px] ' + severityClasses(sev);
        var arrow = document.createElement('span');
        arrow.className = 'text-indigo-600 select-none mr-1';
        arrow.textContent = '>';
        var textNode = document.createElement('span');
        textNode.textContent = text;
        el.appendChild(arrow);
        el.appendChild(textNode);
        logWindow.appendChild(el);

        while (logWindow.children.length > MAX_LINES) {
            logWindow.removeChild(logWindow.firstChild);
        }
        if (autoScroll) logWindow.scrollTop = logWindow.scrollHeight;
    }

    // SSE Connection
    var eventSource = new EventSource('/api/logs');
    eventSource.onmessage = function(event) {
        if (event.data) addLine(event.data);
    };
    eventSource.onerror = function() {
        sseIndicator.innerHTML = '<span class="h-2 w-2 rounded-full bg-red-400"></span><span class="text-[10px] text-red-400 font-bold uppercase tracking-wider">SSE Disconnected</span>';
        addLine('[!] SSE connection lost. Browser will auto-reconnect...');
    };
    eventSource.onopen = function() {
        sseIndicator.innerHTML = '<span class="flex h-2 w-2 relative"><span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span><span class="relative inline-flex rounded-full h-2 w-2 bg-emerald-500"></span></span><span class="text-[10px] text-emerald-400 font-bold uppercase tracking-wider">SSE Connected</span>';
    };

    // Filter Pills
    filterBtns.forEach(function(btn) {
        btn.addEventListener('click', function() {
            filterBtns.forEach(function(b) {
                b.classList.remove('active', 'bg-indigo-950');
                b.classList.add('bg-slate-950/60');
            });
            btn.classList.add('active', 'bg-indigo-950');
            btn.classList.remove('bg-slate-950/60');
            activeFilter = btn.getAttribute('data-filter');
            renderLines();
        });
    });

    // Search
    searchInput.addEventListener('input', function() {
        searchTerm = searchInput.value.toLowerCase();
        renderLines();
    });

    // Auto-Scroll Toggle
    autoscrollBtn.addEventListener('click', function() {
        autoScroll = !autoScroll;
        if (autoScroll) {
            autoscrollBtn.textContent = 'Auto-Scroll: ON';
            autoscrollBtn.classList.remove('bg-slate-950/60', 'text-gray-400', 'border-indigo-950/60');
            autoscrollBtn.classList.add('bg-emerald-950/60', 'text-emerald-300', 'border-emerald-900/60');
            logWindow.scrollTop = logWindow.scrollHeight;
        } else {
            autoscrollBtn.textContent = 'Auto-Scroll: OFF';
            autoscrollBtn.classList.remove('bg-emerald-950/60', 'text-emerald-300', 'border-emerald-900/60');
            autoscrollBtn.classList.add('bg-slate-950/60', 'text-gray-400', 'border-indigo-950/60');
        }
    });

    // Clear Window
    clearBtn.addEventListener('click', function() {
        rawLines = [];
        logWindow.innerHTML = '<div class="text-gray-600 select-none text-[11px]">// Terminal cleared. Listening...</div>';
    });

    // Export Logs
    exportBtn.addEventListener('click', function() {
        var content = rawLines.map(function(l) { return l.text; }).join('\n');
        var blob = new Blob([content], { type: 'text/plain' });
        var url = URL.createObjectURL(blob);
        var a = document.createElement('a');
        a.href = url;
        a.download = 'sawt_logs_' + new Date().toISOString().replace(/[:.]/g, '-') + '.log';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    });
})();
