(function() {
    let ws = null;
    let agents = [];
    let activeAgent = null;

    function initWebSocket() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = protocol + '//' + window.location.host + '/ws/workflow';

        ws = new WebSocket(wsUrl);

        ws.onopen = function() {
            console.log('[SeqWF] Connected to WebSocket at ' + wsUrl);
            updateStatus('Connected', 'emerald');
        };

        ws.onclose = function() {
            console.log('[SeqWF] WebSocket disconnected. Retrying in 3s...');
            updateStatus('Disconnected', 'red');
            setTimeout(initWebSocket, 3000);
        };

        ws.onerror = function(err) {
            console.error('[SeqWF] WebSocket error:', err);
            updateStatus('Error', 'red');
        };

        ws.onmessage = function(event) {
            try {
                const msg = JSON.parse(event.data);
                handleWSMessage(msg);
            } catch (e) {
                console.error('[SeqWF] Error parsing message:', e);
            }
        };
    }

    function updateStatus(text, color) {
        const badge = document.getElementById('seq-status-badge');
        if (badge) {
            badge.innerText = text;
            badge.className = `text-xs font-bold text-${color}-400 font-mono border border-${color}-900 bg-${color}-950/60 px-2 py-0.5 rounded-full`;
        }
    }

    function handleWSMessage(msg) {
        console.log('[SeqWF] Event:', msg.type, msg);

        switch (msg.type) {
            case 'agent_config':
                agents = msg.content || [];
                renderAgentCards(agents);
                break;

            case 'agent_start':
                activeAgent = msg.agent;
                highlightAgent(msg.agent, 'active');
                appendLog(msg.agent, `🚀 Starting phase... ${msg.content || ''}`);
                break;

            case 'agent_thinking':
                highlightAgent(msg.agent, 'thinking');
                appendLog(msg.agent, `🧠 ${msg.content}`);
                break;

            case 'chunk':
                appendChunk(msg.agent, msg.content);
                break;

            case 'agent_complete':
                highlightAgent(msg.agent, 'complete');
                appendLog(msg.agent, `✅ Complete.`);
                break;

            case 'workflow_complete':
                updateStatus('Workflow Finished', 'emerald');
                const runBtn = document.getElementById('seq-run-btn');
                if (runBtn) runBtn.disabled = false;
                break;

            case 'error':
                appendLog('system', `❌ Error: ${msg.content}`);
                const btn = document.getElementById('seq-run-btn');
                if (btn) btn.disabled = false;
                break;
        }
    }

    function renderAgentCards(agentList) {
        const container = document.getElementById('seq-agent-cards');
        if (!container) return;

        container.innerHTML = '';
        agentList.forEach(a => {
            const card = document.createElement('div');
            card.id = `agent-card-${a.name}`;
            card.className = `glass p-4 rounded-xl border border-indigo-950/60 space-y-2 transition duration-300`;
            card.innerHTML = `
                <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                        <span class="text-lg">${a.icon || '🤖'}</span>
                        <span class="font-bold text-white text-sm">${a.displayName || a.name}</span>
                    </div>
                    <span id="agent-status-${a.name}" class="text-[10px] font-semibold uppercase px-2 py-0.5 rounded bg-slate-900 text-gray-400 border border-slate-800">Idle</span>
                </div>
                <p class="text-xs text-gray-400 leading-snug">${a.description || ''}</p>
                <div id="agent-output-${a.name}" class="text-xs font-mono text-slate-300 bg-slate-950/80 p-3 rounded-lg border border-indigo-950/40 min-h-[80px] max-h-[160px] overflow-y-auto whitespace-pre-wrap">Waiting to execute...</div>
            `;
            container.appendChild(card);
        });
    }

    function highlightAgent(agentName, state) {
        const card = document.getElementById(`agent-card-${agentName}`);
        const status = document.getElementById(`agent-status-${agentName}`);
        if (!card || !status) return;

        if (state === 'active' || state === 'thinking') {
            card.className = `glass p-4 rounded-xl border border-indigo-500/80 ring-1 ring-indigo-500/50 space-y-2 transition duration-300 bg-indigo-950/30`;
            status.innerText = state === 'thinking' ? 'Thinking...' : 'Active';
            status.className = `text-[10px] font-semibold uppercase px-2 py-0.5 rounded bg-indigo-950 text-indigo-300 border border-indigo-500 animate-pulse`;
        } else if (state === 'complete') {
            card.className = `glass p-4 rounded-xl border border-emerald-950/80 space-y-2 transition duration-300 bg-emerald-950/10`;
            status.innerText = 'Complete ✓';
            status.className = `text-[10px] font-semibold uppercase px-2 py-0.5 rounded bg-emerald-950 text-emerald-300 border border-emerald-900`;
        }
    }

    function appendLog(agentName, text) {
        const output = document.getElementById(`agent-output-${agentName}`);
        if (output) {
            output.innerText += `\n${text}`;
            output.scrollTop = output.scrollHeight;
        }
    }

    function appendChunk(agentName, chunk) {
        const output = document.getElementById(`agent-output-${agentName}`);
        if (output) {
            if (output.innerText === 'Waiting to execute...') {
                output.innerText = '';
            }
            output.innerText += chunk;
            output.scrollTop = output.scrollHeight;
        }
    }

    window.runSequentialWorkflow = function() {
        const inputEl = document.getElementById('seq-user-input');
        const runBtn = document.getElementById('seq-run-btn');
        if (!inputEl || !ws || ws.readyState !== WebSocket.OPEN) return;

        const val = inputEl.value.trim();
        if (!val) return;

        runBtn.disabled = true;
        updateStatus('Running Pipeline...', 'indigo');

        // Reset agent outputs
        agents.forEach(a => {
            const out = document.getElementById(`agent-output-${a.name}`);
            if (out) out.innerText = 'Waiting...';
            highlightAgent(a.name, 'idle');
        });

        ws.send(JSON.stringify({
            type: 'user_input',
            content: val
        }));
    };

    document.addEventListener('DOMContentLoaded', initWebSocket);
})();
