// ===== i18n Translations =====
const translations = {
    en: {
        title: "Consul Manager",
        consulControl: "Consul Control",
        services: "Services",
        logs: "Logs",
        start: "Start",
        stop: "Stop",
        restart: "Restart",
        openConsul: "Open Consul UI",
        clearLogs: "Clear",
        checking: "Checking...",
        running: "Running",
        stopped: "Stopped",
        unknown: "Unknown",
        enabled: "enabled",
        disabled: "disabled",
        open_service: "Open Web UI",
        tools: "Tools"
    },
    vi: {
        title: "Quản lý Consul",
        consulControl: "Điều khiển Consul",
        services: "Dịch vụ",
        logs: "Nhật ký",
        start: "Khởi động",
        stop: "Dừng",
        restart: "Khởi động lại",
        openConsul: "Mở Consul UI",
        clearLogs: "Xóa",
        checking: "Đang kiểm tra...",
        running: "Đang chạy",
        stopped: "Đã dừng",
        unknown: "Không xác định",
        enabled: "đang bật",
        disabled: "đã tắt",
        open_service: "Mở giao diện Web",
        autoSetup: "Cài đặt tự động",
        setupTitle: "Cấu hình tự động",
        setupDesc: "Nhập đường dẫn gốc để quét dịch vụ",
        confirmSetup: "Bắt đầu quét",
        cancel: "Hủy bỏ",
        tools: "Công cụ"
    }
};

// ===== State =====
let services = [];
let registeredServices = {}; // New: Real status
let ws = null;
let currentLang = 'en';
let currentTheme = 'dark';
let activeTab = 'services';
let setupPath = '';
let logMode = 'realtime'; // 'realtime' or 'files'

// ===== Cache Layer =====
const apiCache = {
    data: new Map(),
    set(key, value, ttl = 3000) {
        const expiry = Date.now() + ttl;
        this.data.set(key, { value, expiry });
    },
    get(key) {
        const cached = this.data.get(key);
        if (!cached) return null;
        if (Date.now() > cached.expiry) {
            this.data.delete(key);
            return null;
        }
        return cached.value;
    },
    clear() {
        this.data.clear();
    }
};

const t = (key) => translations[currentLang]?.[key] || key;

// ===== Init =====
document.addEventListener('DOMContentLoaded', async () => {
    // 1. Fetch persistent settings from server
    await fetchSettings();
    
    // 2. Apply settings
    applyTheme(currentTheme);
    applyLanguage(currentLang);
    
    // Check for hash in URL, otherwise use saved tab
    const hash = window.location.hash.slice(1);
    if (hash && ['services', 'logs', 'tools', 'ports'].includes(hash)) {
        activeTab = hash;
    }
    
    switchTab(activeTab, false); // Don't save on init
    setLogMode(logMode, false);  // Don't save on init
    
    // Restore setup path
    const pathInput = document.getElementById('setup-path');
    if (pathInput) pathInput.value = setupPath;

    loadServices();
    fetchAllStatus(); // Use batch endpoint on init
    connectWebSocket();
    enableDragAndDrop(); // Init Drag & Drop

    // Add Enter key support for Port Scanner
    const scanInput = document.getElementById('scan-ports-input');
    if (scanInput) {
        scanInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') scanPorts();
        });
    }

    // Optimized polling with Page Visibility API
    let pollingInterval = 60000; // 60s when active
    let intervalId;

    const startPolling = () => {
        intervalId = setInterval(async () => {
            // Only poll if page is visible
            if (!document.hidden) {
                await fetchAllStatus(); // Use batch endpoint
                renderServices();
            }
        }, pollingInterval);
    };

    // Adjust polling based on visibility
    document.addEventListener('visibilitychange', () => {
        clearInterval(intervalId);
        if (!document.hidden) {
            // Page visible: poll every 60s
            pollingInterval = 60000;
            apiCache.clear(); // Clear cache when returning
            startPolling();
            // Immediate refresh
            fetchAllStatus();
            renderServices();
        } else {
            // Page hidden: poll every 5 minutes to save resources
            pollingInterval = 300000;
            startPolling();
        }
    });

    startPolling();
    
    // Handle browser back/forward buttons
    window.addEventListener('hashchange', () => {
        const hash = window.location.hash.slice(1);
        if (hash && ['services', 'logs', 'tools'].includes(hash)) {
            switchTab(hash, true);
        }
    });
});

// ===== Tab Functions =====
function switchTab(tab, persist = true) {
    activeTab = tab;
    // Update URL hash without scrolling
    if (window.location.hash.slice(1) !== tab) {
        history.replaceState(null, null, `#${tab}`);
    }
    
    if (persist) saveSettings();

    // Update sidebar nav items
    document.querySelectorAll('.nav-item').forEach(btn => btn.classList.remove('active'));
    const activeBtn = document.getElementById(`tab-${tab}`);
    if (activeBtn) activeBtn.classList.add('active');

    // Update tab content
    document.querySelectorAll('.tab-content').forEach(content => content.classList.remove('active'));
    document.getElementById(`content-${tab}`).classList.add('active');

    // Update header title
    const titleMap = {
        'services': translations[currentLang].services || 'Services',
        'logs': translations[currentLang].logs || 'Logs',
        'tools': translations[currentLang].tools || 'Tools'
    };
    const tabTitle = document.getElementById('tab-title');
    if (tabTitle) tabTitle.textContent = titleMap[tab] || tab.charAt(0).toUpperCase() + tab.slice(1);
    
    // Hide/Show badge
    const badge = document.getElementById('service-count-badge');
    if (badge) badge.style.display = (tab === 'services' ? 'inline-flex' : 'none');

    // Refresh data based on tab
    if (tab === 'logs') {
        renderLogs();
    } else if (tab === 'tools') {
        loadPortStatus();
    } else if (tab === 'services') {
        renderServices();
    }
}

function renderLogs() {
    const viewer = document.getElementById('log-viewer');
    if (viewer) {
        viewer.scrollTop = viewer.scrollHeight;
    }
}

// ===== Theme Functions =====
function toggleTheme() {
    currentTheme = currentTheme === 'dark' ? 'light' : 'dark';
    saveSettings();
    applyTheme(currentTheme);
}

function applyTheme(theme) {
    if (theme === 'light') {
        document.documentElement.setAttribute('data-theme', 'light');
        document.getElementById('icon-sun').style.display = 'none';
        document.getElementById('icon-moon').style.display = 'block';
    } else {
        document.documentElement.removeAttribute('data-theme');
        document.getElementById('icon-sun').style.display = 'block';
        document.getElementById('icon-moon').style.display = 'none';
    }
}

// ===== i18n Functions =====

function setLanguage(lang) {
    currentLang = lang;
    saveSettings();
    applyLanguage(lang);
    renderServices(); // Re-render with new language
}

function applyLanguage(lang) {
    currentLang = lang;
    
    // Update button states
    document.querySelectorAll('.lang-btn, .lang-switcher-small button').forEach(btn => {
        btn.classList.remove('active');
    });
    document.getElementById(`lang-${lang}`)?.classList.add('active');
    
    // Update all translatable elements
    document.querySelectorAll('[data-i18n]').forEach(el => {
        const key = el.getAttribute('data-i18n');
        if (translations[lang][key]) {
            el.textContent = translations[lang][key];
        }
    });

    // Refresh current tab title & count
    switchTab(activeTab, false);
    
    // Update document title
    document.title = translations[lang].title;
}

// ===== Load services from config =====
async function loadServices() {
    try {
        const res = await fetch('/api/services');
        services = await res.json();
        await fetchRegisteredServices(); // Fetch real status
        renderServices();
    } catch (e) {
        console.error('Failed to load services:', e);
    }
}

async function forceReload() {
    const btn = document.getElementById('force-reload');
    if (btn) btn.style.transform = 'rotate(180deg)';

    // Clear all caches on force reload
    apiCache.clear();

    try {
        // Use batch endpoint for faster reload (1 API call instead of 4)
        await Promise.allSettled([
            loadServices(),
            fetchAllStatus()
        ]);
        renderServices();
    } catch (e) {
        console.error('Reload error:', e);
    } finally {
        if (btn) {
            setTimeout(() => {
                btn.style.transform = 'rotate(0deg)';
            }, 300);
        }
    }
}

async function runSetup() {
    openConfig();
    switchConfigTab('scan');
}

function closeSetupModal() {
    document.getElementById('setup-modal').classList.remove('active');
}

async function confirmSetup() {
    const path = document.getElementById('cfg-scan-path').value;
    const logContainer = document.getElementById('scan-log-container');
    
    if (!path) {
        showToast('Please enter a directory to scan', 'error');
        return;
    }

    // Setup Log Container
    logContainer.innerHTML = '';
    const log = (msg, cls = 'log-info') => {
        const div = document.createElement('div');
        div.className = cls;
        div.textContent = msg;
        logContainer.appendChild(div);
        logContainer.scrollTop = logContainer.scrollHeight;
    };

    // Temporarily capture WS logs for scan
    const originalHandler = ws ? ws.onmessage : null;
    const scanMessages = [];
    
    if (ws) {
        ws.onmessage = (event) => {
            const msg = event.data;
            // Filter for setup-related messages
            if (msg.includes('Auto Setup') || msg.includes('Found:') || msg.includes('✅') || 
                msg.includes('✨') || msg.includes('📦') || msg.includes('⚠️') || msg.includes('Special Case')) {
                log(msg, msg.includes('✅') || msg.includes('✨') ? 'log-success' : 
                         msg.includes('⚠️') ? 'log-warning' : 'log-info');
                scanMessages.push(msg);
            }
            // Also call original handler for log viewer
            if (originalHandler) originalHandler(event);
        };
    }

    log(`🔍 Starting scan in: ${path}...`);
    
    setupPath = path;
    saveSettings();

    try {
        const res = await fetch('/api/setup', { 
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path })
        });
        const data = await res.json();
        
        // Wait a moment for WS messages to arrive
        await new Promise(r => setTimeout(r, 500));
        
        if (data.success) {
            if (scanMessages.length === 0) {
                log(`✅ Scan complete! Updated ${data.updated} services.`, 'log-success');
            }
            
            if (data.portConflicts && data.portConflicts.length > 0) {
                log(`⚠️ Found ${data.portConflicts.length} active process(es) on service ports.`, 'log-warning');
                data.portConflicts.forEach(c => {
                    log(`   - Port ${c.port}: ${c.process} (PID ${c.pid})`, 'log-warning');
                });
            }

            // Refresh Config Editor
            const resCfg = await fetch('/api/config');
            const newCfg = await resCfg.json();
            servicesCache = newCfg.services;
            renderConfigServices();
            
            // Update Consul Path in UI if it was auto-detected
            if (newCfg.consulPath) {
                document.getElementById('cfg-consul-path').value = newCfg.consulPath;
            }
             
            // Refresh main
            loadServices();
             
        } else {
            log('❌ Setup failed or no services found.', 'log-error');
        }
    } catch (e) {
        console.error('Auto Setup Error:', e);
        log(`❌ Error: ${e.message}`, 'log-error');
    } finally {
        // Restore original WS handler
        if (ws && originalHandler) {
            ws.onmessage = originalHandler;
        }
    }
}

// ===== Batch fetch all status (optimized single API call) =====
async function fetchAllStatus() {
    const cacheKey = 'all-status';
    const cached = apiCache.get(cacheKey);
    if (cached) {
        applyStatusData(cached);
        return;
    }

    try {
        const res = await fetch('/api/status/all');
        const data = await res.json();
        apiCache.set(cacheKey, data, 2000); // 2s cache
        applyStatusData(data);
    } catch (e) {
        console.error('Failed to fetch status:', e);
    }
}

function applyStatusData(data) {
    // Update Consul Local Status
    if (data.consulLocal) {
        const localStatus = document.getElementById('consul-local-status');
        const headerStatus = document.getElementById('consul-status');
        const headerText = headerStatus?.querySelector('.text');
        const btnStart = document.getElementById('btn-start');
        const btnStop = document.getElementById('btn-stop');
        const btnRestart = document.getElementById('btn-restart');

        const isRunning = data.consulLocal.running || data.consulLocal.apiRunning;

        if (localStatus) {
            localStatus.className = isRunning ? 'status-dot running' : 'status-dot stopped';
            localStatus.title = isRunning ? 'Running' : 'Stopped';
        }

        if (headerStatus) {
            const dot = headerStatus.querySelector('.dot');
            if (dot) {
                dot.className = isRunning ? 'dot online' : 'dot offline';
            }
            if (headerText) headerText.textContent = isRunning ? t('running') : t('stopped');
        }

        if (btnStart && btnStop && btnRestart) {
            btnStart.style.display = isRunning ? 'none' : 'flex';
            btnStop.style.display = isRunning ? 'flex' : 'none';
            btnRestart.style.display = isRunning ? 'flex' : 'none';
        }
    }

    // Update Consul VPS Status
    if (data.consulVPS) {
        const vpsStatus = document.getElementById('consul-vps-status');
        if (vpsStatus) {
            vpsStatus.className = data.consulVPS.running ? 'status-dot running' : 'status-dot stopped';
            vpsStatus.title = data.consulVPS.running ? 'Running' : 'Stopped';
        }
    }

    // Update DB Status
    if (data.db) {
        const dbStatus = document.getElementById('db-vps-status');
        if (dbStatus) {
            dbStatus.className = data.db.running ? 'status-dot running' : 'status-dot stopped';
            dbStatus.title = data.db.running ? 'Connected' : 'Disconnected';
        }
    }

    // Update Registered Services
    if (data.registeredServices) {
        registeredServices = data.registeredServices;
    }
}

// ===== Fetch Registered Services (with cache) =====
async function fetchRegisteredServices() {
    const cacheKey = 'registered-services';
    const cached = apiCache.get(cacheKey);
    if (cached) {
        registeredServices = cached;
        return;
    }

    try {
        const res = await fetch('/api/consul/services');
        registeredServices = await res.json();
        apiCache.set(cacheKey, registeredServices, 2000); // 2s cache
    } catch (e) {
        console.error('Failed to fetch registered services:', e);
        registeredServices = {};
    }
}

// ===== Check DB Status (with cache) =====
async function checkDBStatus() {
    const dbStatus = document.getElementById('db-vps-status');
    if (!dbStatus) return;

    const cacheKey = 'db-status';
    const cached = apiCache.get(cacheKey);

    if (cached) {
        dbStatus.className = cached.running ? 'status-dot running' : 'status-dot stopped';
        dbStatus.title = cached.running ? 'Connected' : 'Disconnected';
        return;
    }

    try {
        const res = await fetch('/api/db/status');
        const data = await res.json();
        apiCache.set(cacheKey, data, 5000); // 5s cache

        if (data.running) {
            dbStatus.className = 'status-dot running';
            dbStatus.title = 'Connected';
        } else {
            dbStatus.className = 'status-dot stopped';
            dbStatus.title = 'Disconnected';
        }
    } catch (e) {
        console.error('DB Check Error:', e);
        dbStatus.className = 'status-dot';
        dbStatus.title = 'Unknown';
    }
}

// ===== Render service grid (optimized) =====
function renderServices() {
    const container = document.getElementById('services-list');
    const countEl = document.getElementById('service-count-badge');

    if (!services || services.length === 0) {
        container.innerHTML = '<div style="color:var(--text-muted); text-align:center; padding:20px;">No services found</div>';
        countEl.textContent = `0/0 ${t('enabled')}`;
        return;
    }

    // Sort: by rank (asc), then by name
    const sorted = [...services].sort((a, b) => {
        const rankA = a.rank || 0;
        const rankB = b.rank || 0;
        if (rankA !== rankB) return rankA - rankB;
        return a.name.localeCompare(b.name);
    });

    const enabledCount = services.filter(s => s.enabled).length;
    countEl.textContent = `${enabledCount}/${services.length} ${t('enabled')}`;

    // Use fragment for better performance
    const fragment = document.createDocumentFragment();

    // Pre-cache DOM queries for better performance
    const registeredServicesValues = Object.values(registeredServices);

    sorted.forEach(s => {
        const currentMode = s.mode || (s.enabled ? 'vps' : 'off');

        // Fuzzy Matching
        const isMatch = (configName, consulSvcName) => {
            if (configName === consulSvcName) return true;
            if (configName === 'main-simp-backend' && (consulSvcName === 'main' || consulSvcName.startsWith('main-'))) return true;
            if (consulSvcName.startsWith(configName + '-')) return true;
            return false;
        };

        const registration = registeredServicesValues.find(rs => isMatch(s.name, rs.Service));
        const isRegistered = !!registration;

        let displayPort = s.vpsPort;
        let displayHost = s.host;
        let statusClass, dotClass;

        if (currentMode === 'local' && isRegistered) {
            displayPort = registration.Port;
            displayHost = registration.Address || '127.0.0.1';
        }

        if (currentMode === 'off') {
            statusClass = 'disabled';
            dotClass = '';
        } else if (currentMode === 'vps') {
            const isVpsUp = s.vpsReached;
            statusClass = isVpsUp ? 'enabled' : 'error';
            dotClass = isVpsUp ? 'running' : 'stopped';
        } else if (currentMode === 'local') {
            if (isRegistered) {
                statusClass = 'enabled';
                dotClass = 'running';
            } else if (s.localRunning) {
                statusClass = 'pending';
                dotClass = 'starting';
            } else {
                statusClass = 'error';
                dotClass = 'stopped';
            }
        }

        // Create DOM elements (more efficient than innerHTML)
        const card = document.createElement('div');
        card.className = `service-card ${statusClass}`;
        card.draggable = true;
        card.innerHTML = `
            <div class="service-info">
                <div class="service-header">
                    <a href="http://${displayHost}:${displayPort}" target="_blank" class="service-link">
                        <span class="service-name">${s.name}</span>
                    </a>
                    <a href="http://${displayHost}:${displayPort}" target="_blank" class="external-link" title="${t('open_service')}">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                            <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path>
                            <polyline points="15 3 21 3 21 9"></polyline>
                            <line x1="10" y1="14" x2="21" y2="3"></line>
                        </svg>
                    </a>
                    <div class="status-dot ${dotClass}" title="${isRegistered ? 'Connected' : 'Not Connected'}"></div>
                </div>
                <span class="service-address">${displayHost}:${displayPort}</span>
            </div>
            <div class="mode-switch">
                ${s.host !== '127.0.0.1' ? `
                <div class="mode-option ${currentMode === 'vps' ? 'active' : ''}"
                     data-mode="vps"
                     onclick="switchMode('${s.name}', 'vps')">VPS</div>
                ` : ''}
                <div class="mode-option ${currentMode === 'off' ? 'active' : ''}"
                     data-mode="off"
                     onclick="switchMode('${s.name}', 'off')">OFF</div>
                ${s.localPath ? `
                <div class="mode-option ${currentMode === 'local' ? 'active' : ''}"
                     data-mode="local"
                     onclick="switchMode('${s.name}', 'local')">LOC</div>
                ` : ''}
            </div>`;
        fragment.appendChild(card);
    });

    // Replace all children at once (single reflow)
    container.innerHTML = '';
    container.appendChild(fragment);

    // Update badge count
    const badge = document.getElementById('service-count-badge');
    if (badge) {
        const enabledCount = services.filter(s => s.localRunning || s.vpsRunning).length;
        badge.textContent = `${enabledCount}/${services.length} ${translations[currentLang].enabled}`;
    }
}

// Request coalescing to prevent rapid duplicate requests
const pendingRequests = new Map();

// ===== Switch Mode (optimized with request coalescing) =====
async function switchMode(name, mode) {
    // Check if already pending
    const requestKey = `mode-${name}`;
    if (pendingRequests.has(requestKey)) {
        return; // Skip duplicate request
    }

    // Optimistic UI update
    const service = services.find(s => s.name === name);
    if (service) {
        service.mode = mode;
        service.enabled = (mode === 'vps' || mode === 'local');
        renderServices();
    }

    // Mark as pending
    pendingRequests.set(requestKey, true);

    try {
        const res = await fetch('/api/services/mode', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name, mode })
        });

        if (!res.ok) {
            const errText = await res.text();
            throw new Error(errText || 'Request failed');
        }

        // WebSocket will notify us of the update, no need to reload manually
    } catch (e) {
        console.error('Failed to switch mode:', e);
        showToast(e.message, 'error');
        loadServices(); // Reload on error to revert optimistic UI
    } finally {
        // Remove from pending after 1s to allow new requests
        setTimeout(() => pendingRequests.delete(requestKey), 1000);
    }
}


// ===== Consul actions (with request coalescing) =====
async function consulAction(action) {
    // Prevent rapid duplicate consul actions
    const requestKey = `consul-${action}`;
    if (pendingRequests.has(requestKey)) {
        return;
    }

    const btn = document.getElementById(`btn-${action}`);
    const originalContent = btn.innerHTML;

    pendingRequests.set(requestKey, true);

    try {
        btn.disabled = true;
        btn.innerHTML = `<span>...</span>`;

        await fetch(`/api/consul/${action}`, { method: 'POST' });

        setTimeout(() => {
            fetchAllStatus(); // Use batch endpoint
            btn.disabled = false;
            btn.innerHTML = originalContent;
            pendingRequests.delete(requestKey);
        }, 1000);
    } catch (e) {
        console.error(`Failed to ${action} consul:`, e);
        btn.disabled = false;
        btn.innerHTML = originalContent;
        pendingRequests.delete(requestKey);
    }
}

// ===== Check Consul status =====
async function checkConsulStatus() {
    // Check Local Consul
    try {
        const res = await fetch('/api/consul/status');
        const data = await res.json();
        const localStatus = document.getElementById('consul-local-status');
        const headerStatus = document.getElementById('consul-status');
        const headerText = headerStatus?.querySelector('.text');
        const btnStart = document.getElementById('btn-start');
        const btnStop = document.getElementById('btn-stop');
        const btnRestart = document.getElementById('btn-restart');
        
        const isRunning = data.running || data.apiRunning;
        
        if (isRunning) {
            localStatus.className = 'status-dot running';
            localStatus.title = 'Running';
            if (headerStatus) {
                headerStatus.className = 'status running';
                if (headerText) headerText.textContent = t('running');
            }
            // Show stop/restart, hide start
            btnStart.style.display = 'none';
            btnStop.style.display = 'flex';
            btnRestart.style.display = 'flex';
        } else {
            localStatus.className = 'status-dot stopped';
            localStatus.title = 'Stopped';
            if (headerStatus) {
                headerStatus.className = 'status stopped';
                if (headerText) headerText.textContent = t('stopped');
            }
            // Show start, hide stop/restart
            btnStart.style.display = 'flex';
            btnStop.style.display = 'none';
            btnRestart.style.display = 'none';
        }
    } catch (e) {
        const localStatus = document.getElementById('consul-local-status');
        localStatus.className = 'status-dot stopped';
        localStatus.title = 'Unknown';
        // Show start button on error
        document.getElementById('btn-start').style.display = 'flex';
        document.getElementById('btn-stop').style.display = 'none';
        document.getElementById('btn-restart').style.display = 'none';
    }
    
    // Check VPS Consul
    checkVPSConsul();
}

// ===== Check VPS Consul status =====
async function checkVPSConsul() {
    const vpsStatus = document.getElementById('consul-vps-status');
    try {
        const res = await fetch('/api/consul/vps-status');
        const data = await res.json();
        
        console.log('VPS Status Check:', data);
        
        if (data.running) {
            vpsStatus.className = 'status-dot running';
            vpsStatus.title = 'Running';
        } else {
            vpsStatus.className = 'status-dot stopped';
            vpsStatus.title = 'Stopped';
        }
    } catch (e) {
        console.error('VPS Check Error:', e);
        vpsStatus.className = 'status-dot';
        vpsStatus.title = 'Unknown';
    }
}

// ===== WebSocket for logs and updates =====
function connectWebSocket() {
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${location.host}/ws/logs`);

    ws.onopen = () => {
        addLog('🔌 WebSocket connected');
    };

    ws.onmessage = (event) => {
        try {
            // Try to parse as JSON for service updates
            const data = JSON.parse(event.data);
            if (data.type === 'service_update') {
                // Clear cache and refresh services on update
                apiCache.clear();
                loadServices();
                return;
            }
            if (data.type === 'error') {
                if (data.code === 'CONSUL_BINARY_ERROR' && data.message.includes('exec format error')) {
                    showToastWithAction(data.message, 'Consul Binary Error', '🛠️ Fix It', installConsul);
                } else {
                    showToast(data.message, 'error', data.code);
                }
                addLog(`❌ [Error] ${data.message}`);
                return;
            }
        } catch (e) {
            // Not JSON, treat as log message
            addLog(event.data);
        }
    };

    ws.onerror = () => {
        addLog('⚠️ WebSocket error');
    };

    ws.onclose = () => {
        addLog('🔌 WebSocket disconnected, reconnecting in 2s...');
        setTimeout(connectWebSocket, 2000);
    };
}

// ===== Drag & Drop Logic =====
let draggedItem = null;

function enableDragAndDrop() {
    const list = document.getElementById('services-list');
    
    list.addEventListener('dragstart', (e) => {
        draggedItem = e.target.closest('.service-card');
        if (draggedItem) {
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', '');
            setTimeout(() => draggedItem.classList.add('dragging'), 0);
        }
    });

    list.addEventListener('dragover', (e) => {
        e.preventDefault(); // allow drop
    });

    list.addEventListener('dragenter', (e) => {
        e.preventDefault();
        const target = e.target.closest('.service-card');
        if (target && target !== draggedItem && draggedItem) {
            swapNodes(draggedItem, target);
        }
    });

    list.addEventListener('dragend', () => {
        if (draggedItem) {
            draggedItem.classList.remove('dragging');
            draggedItem = null;
            saveNewOrder();
        }
    });
}

function swapNodes(n1, n2) {
    const p1 = n1.parentNode;
    const s1 = n1.nextSibling;
    const p2 = n2.parentNode;
    const s2 = n2.nextSibling;

    if (s1 === n2) {
        p1.insertBefore(n2, n1);
    } else if (s2 === n1) {
        p2.insertBefore(n1, n2); 
    } else {
        p1.insertBefore(n2, s1);
        p2.insertBefore(n1, s2);
    }
}

async function saveNewOrder() {
    const cards = document.querySelectorAll('.service-card');
    const newOrder = [];
    cards.forEach((card, index) => {
        const name = card.querySelector('.service-name').textContent;
        newOrder.push({
            name: name,
            rank: (index + 1) * 10
        });
    });

    // Optimistic update locally
    newOrder.forEach(item => {
        const s = services.find(s => s.name === item.name);
        if (s) s.rank = item.rank;
    });

    try {
        await fetch('/api/services/reorder', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(newOrder)
        });
    } catch (e) {
        console.error('Failed to save order', e);
    }
}

// ===== Add log line (optimized) =====
function addLog(message, force = false) {
    // If in file mode, don't append real-time logs from WS unless forced
    if (logMode === 'files' && !force && !message.startsWith('📂') && !message.startsWith('⏳') && !message.startsWith('✅') && !message.startsWith('❌')) {
        return;
    }

    const viewer = document.getElementById('log-viewer');
    const line = document.createElement('div');
    line.className = 'log-line';

    // Create text span
    const textSpan = document.createElement('span');
    textSpan.className = 'log-text';
    textSpan.textContent = message;

    // Create copy button for this line
    const copyBtn = document.createElement('button');
    copyBtn.className = 'line-copy-btn';
    copyBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>';
    copyBtn.title = 'Copy line';
    copyBtn.onclick = async (e) => {
        e.stopPropagation();
        try {
            await navigator.clipboard.writeText(message);
            copyBtn.innerHTML = '✅';
            setTimeout(() => {
                copyBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>';
            }, 1000);
        } catch (err) { console.error(err); }
    };

    line.appendChild(textSpan);
    line.appendChild(copyBtn);

    // Color based on content (optimized with single check)
    const lowerMsg = message.toLowerCase();
    if (lowerMsg.includes('error') || message.includes('❌')) {
        line.classList.add('error');
    } else if (lowerMsg.includes('warn') || message.includes('⚠')) {
        line.classList.add('warn');
    } else if (lowerMsg.includes('info') || message.includes('✅') || message.includes('🚀') || message.includes('🛡️') || message.includes('🔍')) {
        line.classList.add('info');
    }

    // Apply current filter
    const filterInput = document.getElementById('log-filter');
    const filter = filterInput ? filterInput.value.toLowerCase() : '';
    if (filter && !lowerMsg.includes(filter)) {
        line.style.display = 'none';
    }

    viewer.appendChild(line);

    // Auto scroll - Always pull to bottom if not manually scrolling up
    viewer.scrollTop = viewer.scrollHeight;

    // Limit lines to 1000 for better performance (increased from 50)
    while (viewer.children.length > 1000) {
        viewer.removeChild(viewer.firstChild);
    }
}

// Debounce helper function
function debounce(func, wait) {
    let timeout;
    return function executedFunction(...args) {
        const later = () => {
            clearTimeout(timeout);
            func(...args);
        };
        clearTimeout(timeout);
        timeout = setTimeout(later, wait);
    };
}

// Debounced filter function for better performance
const debouncedFilterLogs = debounce(() => {
    const filter = document.getElementById('log-filter').value.toLowerCase();
    const lines = document.querySelectorAll('.log-line');

    lines.forEach(line => {
        const text = line.textContent.toLowerCase();
        line.style.display = text.includes(filter) ? 'block' : 'none';
    });
}, 300); // 300ms debounce delay

function filterLogs() {
    debouncedFilterLogs();
}

async function copyLogs() {
    const lines = document.querySelectorAll('.log-line');
    const text = Array.from(lines)
        .filter(l => l.style.display !== 'none')
        .map(l => l.textContent)
        .join('\n');
    
    try {
        await navigator.clipboard.writeText(text);
        const btn = document.querySelector('.log-controls .btn-small');
        const originalText = btn.innerHTML;
        btn.innerHTML = '✅ Copied!';
        setTimeout(() => { btn.innerHTML = originalText; }, 2000);
    } catch (err) {
        console.error('Failed to copy logs', err);
    }
}

// ===== Clear logs =====
function clearLogs() {
    document.getElementById('log-viewer').innerHTML = '';
}

// ===== Log Browser Functions =====

function setLogMode(mode, persist = true) {
    logMode = mode;
    if (persist) saveSettings();
    
    // Update UI
    document.querySelectorAll('.log-mode-btn').forEach(btn => btn.classList.remove('active'));
    document.getElementById(`mode-${mode}`).classList.add('active');
    
    const selector = document.getElementById('file-selector-container');
    const controls = document.querySelector('.log-controls');
    
    if (mode === 'files') {
        selector.style.display = 'block';
        controls.style.display = 'none'; // Hide search/copy for file view (or adapt later)
        loadLogFilesList();
    } else {
        selector.style.display = 'none';
        controls.style.display = 'flex';
        clearLogs();
        addLog('🔌 [Real-time] Logs resumed...');
    }
}

async function loadLogFilesList() {
    try {
        const res = await fetch('/api/logs/list');
        const files = await res.json();
        const select = document.getElementById('log-file-list');
        
        select.innerHTML = '<option value="">-- Select a log file --</option>' + 
            files.map(f => `<option value="${f}">${f}</option>`).join('');
            
        clearLogs();
        addLog('📂 [Log Browser] Select a file to view historical logs');
    } catch (e) {
        console.error('Failed to load logs list:', e);
    }
}

async function loadLogFile() {
    const filename = document.getElementById('log-file-list').value;
    if (!filename) return;

    try {
        clearLogs();
        addLog(`⏳ [Loading] ${filename}...`);

        const res = await fetch(`/api/logs/view?file=${encodeURIComponent(filename)}`);
        const content = await res.text();

        clearLogs();
        const lines = content.split('\n').filter(line => line.trim());

        // Batch rendering for better performance with large files
        const BATCH_SIZE = 100;
        let currentIndex = 0;

        const renderBatch = () => {
            const endIndex = Math.min(currentIndex + BATCH_SIZE, lines.length);
            for (let i = currentIndex; i < endIndex; i++) {
                addLog(lines[i], true);
            }
            currentIndex = endIndex;

            if (currentIndex < lines.length) {
                // Use requestAnimationFrame to avoid blocking UI
                requestAnimationFrame(renderBatch);
            } else {
                addLog(`✅ [Loaded] ${filename} (${lines.length} lines)`);
            }
        };

        requestAnimationFrame(renderBatch);
    } catch (e) {
        console.error('Failed to view log file:', e);
        addLog(`❌ [Error] Failed to load ${filename}`);
    }
}

// ===== Persistent Settings API =====

async function fetchSettings() {
    try {
        const res = await fetch('/api/settings');
        const data = await res.json();
        if (data) {
            currentTheme = data.theme || 'dark';
            currentLang = data.language || 'en';
            activeTab = data.activeTab || 'services';
            setupPath = data.setupPath || '';
            logMode = data.logMode || 'realtime';
        }
    } catch (e) {
        console.error('Failed to fetch settings:', e);
    }
}

async function saveSettings() {
    try {
        await fetch('/api/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                theme: currentTheme,
                language: currentLang,
                activeTab: activeTab,
                setupPath: setupPath,
                logMode: logMode
            })
        });
    } catch (e) {
        console.error('Failed to save settings:', e);
    }
}

// ===== Toast Notification =====
function showToast(message, type = 'info', title = '') {
    const container = document.getElementById('toast-container');
    if (!container) return;

    const toast = document.createElement('div');
    toast.className = `toast ${type}`;
    
    // Default titles
    if (!title) {
        if (type === 'error') title = 'Error';
        else if (type === 'success') title = 'Success';
        else title = 'Info';
    }

    toast.innerHTML = `
        <div class="toast-content">
            <div class="toast-title">${title}</div>
            <div class="toast-message">${message}</div>
        </div>
        <button class="toast-close" onclick="this.parentElement.remove()">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <line x1="18" y1="6" x2="6" y2="18"></line>
                <line x1="6" y1="6" x2="18" y2="18"></line>
            </svg>
        </button>
    `;

    container.appendChild(toast);

    // Auto remove after 5s
    setTimeout(() => {
        toast.classList.add('closing');
        setTimeout(() => toast.remove(), 300);
    }, 5000);
}

// ===== Helper Tools Logic =====
// No open/close helper functions needed as it is a tab now

async function loadPortStatus() {
    const container = document.getElementById('ports-grid');
    if (!container) return;

    container.innerHTML = '<div class="port-column-empty"><p>Scanning...</p></div>';
    
    try {
        const res = await fetch('/api/helper/ports');
        const data = await res.json();
        
        if (data.length === 0) {
            container.innerHTML = '<div class="port-column-empty"><p>No ports configured</p></div>';
            return;
        }

        const fragment = document.createDocumentFragment();
        
        data.forEach(item => {
            const s = services.find(svc => svc.name === item.service);
            const isManagedRunning = s && s.localRunning;
            
            let statusClass = 'disabled'; // Gray
            let dotClass = '';
            let statusText = 'Free';

            if (item.inUse) {
                if (isManagedRunning) {
                    statusClass = 'enabled'; // Green
                    dotClass = 'running';
                    statusText = 'Managed';
                } else {
                    statusClass = 'warning'; // Yellow
                    dotClass = 'starting';
                    statusText = 'External/Inactive';
                }
            }

            const card = document.createElement('div');
            card.className = `service-card ${statusClass}`;
            card.style.cursor = 'default';
            card.innerHTML = `
                <div class="service-info">
                    <div class="service-header">
                        <span class="service-name">:${item.port}</span>
                        <div class="status-dot ${dotClass}" title="${statusText}"></div>
                    </div>
                    <span class="service-address" style="color: var(--text-secondary)">${item.service}</span>
                    ${item.inUse ? `
                        <div class="port-card-process" style="font-size: 0.75rem; color: var(--text-muted); display:flex; align-items:center; gap:4px; margin-top:8px;">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M9 9h6v6H9z"/></svg>
                            ${item.process || 'Unknown'} (PID: ${item.pid})
                        </div>
                    ` : ''}
                </div>
                ${item.inUse ? `
                    <div style="margin-top: 12px; display: flex; justify-content: flex-end;">
                        <button onclick="killPort('${item.pid}', ${item.port})" class="btn-small danger" style="color: var(--accent-red); border-color: rgba(255,0,0,0.1); font-size: 0.75rem; padding: 4px 10px; display: flex; align-items: center; gap: 6px;">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor" stroke="none"><rect x="4" y="4" width="16" height="16" rx="2" ry="2"></rect></svg>
                            Stop
                        </button>
                    </div>
                ` : ''}
            `;
            fragment.appendChild(card);
        });

        container.innerHTML = '';
        container.appendChild(fragment);
        
    } catch (e) {
        container.innerHTML = `<div class="port-column-empty" style="color:var(--accent-red);">Error: ${e.message}</div>`;
    }
}

async function killPort(pid, port) {
    if (!confirm(`Are you sure you want to kill process ${pid} on port ${port}?`)) return;

    try {
        const res = await fetch('/api/helper/kill', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ pid, port })
        });
        
        if (res.ok) {
            showToast(`Killed process ${pid}`, 'success');
            // Wait a bit for process to die before refreshing
            setTimeout(loadPortStatus, 1000);
        } else {
            showToast('Failed to kill process', 'error');
        }
    } catch (e) {
        showToast(`Error: ${e.message}`, 'error');
    }
}

// ===== Config Editor Logic =====
let currentConfig = {};
let servicesCache = []; // Local copy for editing

async function openConfig() {
    document.getElementById('config-modal').classList.add('active');
    
    try {
        const res = await fetch('/api/config');
        currentConfig = await res.json();
        servicesCache = currentConfig.services || [];
        
        // Populate General Tab
        document.getElementById('cfg-consul-path').value = currentConfig.consulPath || '';
        document.getElementById('cfg-scan-path').value = currentConfig.defaultScanPath || '';
        
        // Render Services
        renderConfigServices();
        
        // Reset to first tab
        switchConfigTab('scan');
        
        // Hide editor
        document.getElementById('service-editor').style.display = 'none';
        
    } catch (e) {
        showToast('Failed to load config: ' + e.message, 'error');
    }
}

function closeConfig() {
    document.getElementById('config-modal').classList.remove('active');
}

function switchConfigTab(tabName) {
    // Buttons - remove active from all, add to clicked one
    document.querySelectorAll('.config-tab-btn').forEach(btn => {
        btn.classList.remove('active');
        // Check if this button triggers this tabName
        if (btn.getAttribute('onclick')?.includes(`'${tabName}'`)) {
            btn.classList.add('active');
        }
    });
    
    // Content
    document.querySelectorAll('.config-tab-content').forEach(c => c.classList.remove('active'));
    document.getElementById(`config-tab-${tabName}`).classList.add('active');
}

// Service CRUD
function renderConfigServices() {
    const tbody = document.getElementById('cfg-services-list');
    tbody.innerHTML = servicesCache.map((s, idx) => `
        <tr>
            <td>${s.name}</td>
            <td>${s.vpsPort}</td>
            <td>${s.localPort || '-'}</td>
            <td title="${s.localPath}" style="max-width: 150px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">
                ${s.localPath || '<span style="color:var(--text-muted)">-</span>'}
            </td>
            <td>
                <button onclick="editService(${idx})" class="btn-small" style="padding: 2px 8px;">Edit</button>
                <button onclick="deleteService(${idx})" class="btn-small" style="padding: 2px 8px; color: var(--accent-red); border-color: rgba(255,0,0,0.3);">Del</button>
            </td>
        </tr>
    `).join('');
}

function addService() {
    document.getElementById('service-editor').style.display = 'block';
    
    // Clear form
    document.getElementById('edit-original-name').value = '';
    document.getElementById('edit-name').value = '';
    document.getElementById('edit-host').value = 'localhost';
    document.getElementById('edit-port').value = '';
    document.getElementById('edit-def-port').value = '';
    document.getElementById('edit-local-path').value = '';
    document.getElementById('edit-cmd').value = '';
    
    // Scroll to editor
    document.getElementById('service-editor').scrollIntoView({ behavior: 'smooth' });
}

function editService(idx) {
    const svc = servicesCache[idx];
    document.getElementById('service-editor').style.display = 'block';
    
    document.getElementById('edit-original-name').value = idx; // Use index to track
    document.getElementById('edit-name').value = svc.name;
    document.getElementById('edit-host').value = svc.host;
    document.getElementById('edit-port').value = svc.port;
    document.getElementById('edit-def-port').value = svc.localPort || '';
    document.getElementById('edit-local-path').value = svc.localPath || '';
    document.getElementById('edit-cmd').value = svc.cmd || '';
    
    document.getElementById('service-editor').scrollIntoView({ behavior: 'smooth' });
}

function deleteService(idx) {
    if (!confirm('Delete this service?')) return;
    servicesCache.splice(idx, 1);
    renderConfigServices();
}

function cancelEditService() {
    document.getElementById('service-editor').style.display = 'none';
}

function saveServiceChanges() {
    const originalIdx = document.getElementById('edit-original-name').value;
    const name = document.getElementById('edit-name').value.trim();
    
    if (!name) {
        showToast('Service name is required', 'error');
        return;
    }
    
    const newService = {
        name: name,
        host: document.getElementById('edit-host').value.trim(),
        port: parseInt(document.getElementById('edit-port').value) || 0,
        localPort: parseInt(document.getElementById('edit-def-port').value) || 0,
        localPath: document.getElementById('edit-local-path').value.trim(),
        cmd: document.getElementById('edit-cmd').value.trim(),
        // Preserve other fields if editing
        mode: (originalIdx !== '' && servicesCache[originalIdx]) ? servicesCache[originalIdx].mode : 'local',
        enabled: true
    };
    
    if (originalIdx !== '') {
        // Update existing
        const idx = parseInt(originalIdx);
        // Merge with existing to keep status/rank
        servicesCache[idx] = { ...servicesCache[idx], ...newService };
    } else {
        // Add new
        servicesCache.push(newService);
    }
    
    renderConfigServices();
    cancelEditService();
}

async function saveFullConfig() {
    // 1. Update config object
    currentConfig.consulPath = document.getElementById('cfg-consul-path').value.trim();
    currentConfig.services = servicesCache;
    
    // 2. Send to backend
    try {
        const res = await fetch('/api/config', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(currentConfig)
        });
        
        if (res.ok) {
            showToast('Configuration saved successfully!', 'success');
            closeConfig();
            loadServices(); // Refresh main view
        } else {
            showToast('Failed to save configuration', 'error');
        }
    } catch (e) {
        showToast('Error saving: ' + e.message, 'error');
    }
}

// ===== Port Scanner Logic =====
let currentScanResults = [];

async function scanPorts() {
    const input = document.getElementById('scan-ports-input');
    const tbody = document.getElementById('scanner-tbody');
    const resultsDiv = document.getElementById('scanner-results');
    const btnKillAll = document.getElementById('btn-kill-all');
    
    // Clear previous results immediately to show loading state better
    const portsStr = input.value.trim();
    if (!portsStr) {
        showToast('Please enter ports to scan', 'error');
        return;
    }

    const ports = portsStr.split(/[\s,]+/).map(p => parseInt(p)).filter(p => !isNaN(p) && p > 0);
    
    if (ports.length === 0) {
        showToast('Invalid port format', 'error');
        return;
    }

    // Show loading
    resultsDiv.classList.remove('hidden');
    tbody.innerHTML = '<tr><td colspan="4" style="text-align:center; padding: 20px;">Scanning...</td></tr>';
    btnKillAll.style.display = 'none';

    try {
        const res = await fetch('/api/helper/scan-active', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ports })
        });
        
        currentScanResults = await res.json();
        renderScanResults();

    } catch (e) {
        console.error('Scan failed:', e);
        showToast('Scan failed: ' + e.message, 'error');
        tbody.innerHTML = '<tr><td colspan="4" style="text-align:center; color: var(--accent-red);">Scan failed</td></tr>';
    }
}

function renderScanResults() {
    const tbody = document.getElementById('scanner-tbody');
    const btnKillAll = document.getElementById('btn-kill-all');
    tbody.innerHTML = '';

    const activeItems = currentScanResults.filter(r => r.inUse);

    if (activeItems.length === 0) {
        tbody.innerHTML = '<tr><td colspan="4" style="text-align:center; padding: 20px; color: var(--text-muted);">No active processes found on these ports</td></tr>';
        btnKillAll.style.display = 'none';
        return;
    }

    btnKillAll.style.display = 'flex';

    activeItems.forEach(item => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td class="port-cell">${item.port}</td>
            <td class="pid-cell">${item.pid}</td>
            <td>${item.process || 'Unknown'}</td>
            <td>
                <button onclick="killProcess('${item.pid}', ${item.port})" class="action-btn sm danger" title="Stop Process" style="display: flex; align-items: center; gap: 6px; padding: 4px 10px;">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="none"><rect x="4" y="4" width="16" height="16" rx="2" ry="2"></rect></svg>
                    <span style="font-weight: 500;">Stop</span>
                </button>
            </td>
        `;
        tbody.appendChild(tr);
    });
}

async function killProcess(pid, port) {
    if (!confirm(`Are you sure you want to kill process ${pid} on port ${port}?`)) return;

    try {
        const res = await fetch('/api/helper/kill', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ pid, port })
        });

        if (res.ok) {
            showToast(`Killed process ${pid}`, 'success');
            // Remove from list or re-scan
            currentScanResults = currentScanResults.map(r => {
                if (r.pid === pid) return { ...r, inUse: false, pid: '', process: '' };
                return r;
            });
            renderScanResults();
        } else {
            showToast('Failed to kill process', 'error');
        }
    } catch (e) {
        showToast('Error: ' + e.message, 'error');
    }
}

async function killAllProcesses() {
    const activeItems = currentScanResults.filter(r => r.inUse);
    if (activeItems.length === 0) return;
    
    if (!confirm(`Are you sure you want to kill ALL ${activeItems.length} processes? This cannot be undone.`)) return;

    let successCount = 0;
    for (const item of activeItems) {
        try {
            const res = await fetch('/api/helper/kill', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ pid: item.pid, port: item.port })
            });
            if (res.ok) successCount++;
        } catch (e) {
            console.error(e);
        }
    }
    
    showToast(`Batch kill completed. Killed ${successCount}/${activeItems.length} processes.`, 'success');
    scanPorts(); // Re-scan to verify
}

async function installConsul() {
    if (!confirm('This will download and install the correct Consul binary for macOS. Continue?')) return;
    
    showToast('Downloading and installing Consul...', 'info');
    
    try {
        const res = await fetch('/api/consul/install', { method: 'POST' });
        const data = await res.json();
        if (data.success) {
            showToast('Consul installed successfully! You can now start it.', 'success');
        } else {
            showToast('Installation failed. Check logs.', 'error');
        }
    } catch (e) {
        showToast('Error: ' + e.message, 'error');
    }
}

function showToastWithAction(message, title, actionText, actionCallback) {
    const container = document.getElementById('toast-container');
    if (!container) return;

    const toast = document.createElement('div');
    toast.className = 'toast error'; // Use error style but with action
    
    toast.innerHTML = `
        <div class="toast-content">
            <div class="toast-title">${title}</div>
            <div class="toast-message">${message}</div>
            <button class="btn-small" id="toast-action-btn" style="margin-top:8px; background:white; color:var(--text-main); border:none;">${actionText}</button>
        </div>
        <button class="toast-close" onclick="this.parentElement.remove()">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
        </button>
    `;

    container.appendChild(toast);
    
    document.getElementById('toast-action-btn').onclick = (e) => {
        e.stopPropagation();
        actionCallback();
        toast.remove();
    };

    // Auto remove after 10s (longer for action)
    setTimeout(() => {
        if (toast.parentElement) {
            toast.classList.add('closing');
            setTimeout(() => toast.remove(), 300);
        }
    }, 10000);
}

