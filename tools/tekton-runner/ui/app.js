const healthBadge = document.getElementById("healthBadge");
const activityEl = document.getElementById("activity");
const workspaceListEl = document.getElementById("workspaceList");
const workspaceDetailEl = document.getElementById("workspaceDetail");
const runStatusEl = document.getElementById("runStatus");
const runLogEl = document.getElementById("runLog");
const runTaskNameEl = document.getElementById("runTaskName");
const runTaskNsEl = document.getElementById("runTaskNs");
const runTaskTailEl = document.getElementById("runTaskTail");
const e2eWorkspaceEl = document.getElementById("e2eWorkspace");
const e2eAppEl = document.getElementById("e2eApp");
const e2eRunIdEl = document.getElementById("e2eRunId");
const e2eLimitEl = document.getElementById("e2eLimit");
const e2eTailRawEl = document.getElementById("e2eTailRaw");
const e2eFormatEl = document.getElementById("e2eFormat");
const e2eIncludeTaskrunEl = document.getElementById("e2eIncludeTaskrun");
const e2eIncludeContainersEl = document.getElementById("e2eIncludeContainers");
const e2eStatusEl = document.getElementById("e2eStatus");
const e2eLogEl = document.getElementById("e2eLog");

let currentWorkspace = null;
let currentApp = null;
let workspacesCache = [];
let activityCache = [];
let wsStatusCache = {};
let wsMetricsCache = {};
let wsMetricsInFlight = {};
let endpointCache = {};
let endpointInFlight = {};
let wsPollTimer = null;
let runPollTimer = null;
let runState = { name: "", namespace: "", status: "", message: "" };
let runStreamController = null;
let runLogBuffer = "";
let podLogController = null;
let podLogBuffer = "";
let podLogKey = "";
let podLogTargetEl = null;
let podLogState = { workspace: "", pod: "", tail: "500", active: false };
let hostInfo = { host_ip: "" };
let externalMap = [];
let externalInFlight = {};
let externalDraft = {};
let lastRunId = "";

function addActivity(entry) {
  activityCache.unshift(entry);
  activityCache = activityCache.slice(0, 20);
  renderActivity();
}

function renderActivity() {
  activityEl.innerHTML = "";
  activityCache.forEach((entry) => {
    const card = document.createElement("div");
    card.className = "activity-card";

    const head = document.createElement("div");
    head.className = "activity-head";

    const left = document.createElement("div");
    left.textContent = `${entry.method} ${entry.endpoint}`;

    const tag = document.createElement("div");
    tag.className = `tag ${entry.status >= 200 && entry.status < 300 ? "ok" : entry.status >= 400 && entry.status < 500 ? "warn" : "err"}`;
    tag.textContent = entry.status;

    head.append(left, tag);

    const msg = document.createElement("div");
    msg.className = "activity-msg";
    msg.textContent = entry.message;

    card.append(head, msg);
    activityEl.append(card);
  });
}

async function api(path, options = {}) {
  const res = await fetch(path, options);
  const contentType = res.headers.get("content-type") || "";
  const body = contentType.includes("application/json") ? await res.json() : await res.text();
  return { status: res.status, body };
}

function toMessage(body) {
  if (typeof body === "string") return body;
  if (!body) return "";
  if (body.message) return body.message;
  if (body.status) return body.status;
  return "OK";
}

async function handleRequest(method, endpoint, options, quiet = false) {
  const res = await api(endpoint, options);
  if (!quiet) {
    addActivity({
      method,
      endpoint,
      status: res.status,
      message: toMessage(res.body)
    });
  }
  return res;
}

function setRunContext(name, namespace) {
  runState = { name: name || "", namespace: namespace || "", status: "", message: "" };
  if (runTaskNameEl) runTaskNameEl.value = runState.name;
  if (runTaskNsEl) runTaskNsEl.value = runState.namespace || "tekton-pipelines";
  startRunPoll();
  startRunStream();
}

function stopRunPoll() {
  if (runPollTimer) {
    clearInterval(runPollTimer);
    runPollTimer = null;
  }
}

function startRunPoll() {
  stopRunPoll();
  if (!runState.name) return;
  refreshRunStatus(true);
  runPollTimer = setInterval(() => {
    refreshRunStatus(true);
  }, 2000);
}

function renderRunStatus() {
  if (!runStatusEl) return;
  if (!runState.name) {
    runStatusEl.textContent = "No TaskRun selected.";
    return;
  }
  const status = runState.status || "Unknown";
  const msg = runState.message || "-";
  runStatusEl.textContent = `TaskRun: ${runState.name} | Status: ${status} | ${msg}`;
}

async function refreshRunStatus(quiet = false) {
  if (!runState.name) return;
  const ns = runTaskNsEl?.value?.trim() || runState.namespace || "tekton-pipelines";
  const res = await handleRequest("GET", `/taskrun/status?name=${encodeURIComponent(runState.name)}&namespace=${encodeURIComponent(ns)}`, {}, quiet);
  if (res.status === 200) {
    runState.namespace = ns;
    runState.status = res.body?.status || "";
    runState.message = res.body?.message || res.body?.reason || "";
    renderRunStatus();
    if (runState.status === "True" || runState.status === "False") {
      stopRunPoll();
    }
  }
}

async function refreshRunLogs(quiet = false) {
  if (!runState.name || !runLogEl) return;
  startRunStream();
}

function syncE2EInputs() {
  if (e2eWorkspaceEl && currentWorkspace) e2eWorkspaceEl.value = currentWorkspace;
  if (e2eAppEl && currentApp) e2eAppEl.value = currentApp;
  if (e2eRunIdEl && lastRunId) e2eRunIdEl.value = lastRunId;
}

function buildE2ELogsEndpoint() {
  const workspace = e2eWorkspaceEl?.value?.trim() || "";
  const app = e2eAppEl?.value?.trim() || "";
  const runId = e2eRunIdEl?.value?.trim() || "";
  const limit = e2eLimitEl?.value?.trim() || "200";
  const tailRaw = e2eTailRawEl?.value?.trim() || "300";
  const format = e2eFormatEl?.value || "text";
  const includeTaskrun = e2eIncludeTaskrunEl?.value || "true";
  const includeContainers = e2eIncludeContainersEl?.value || "true";
  if (!workspace) {
    throw new Error("Workspace zorunlu.");
  }
  const q = new URLSearchParams();
  q.set("workspace", workspace);
  if (app) q.set("app", app);
  if (runId) q.set("run_id", runId);
  q.set("limit", limit);
  q.set("tail_raw", tailRaw);
  q.set("format", format);
  q.set("include_taskrun", includeTaskrun);
  q.set("include_containers", includeContainers);
  return `/run/logs?${q.toString()}`;
}

async function fetchE2ELogs() {
  if (!e2eLogEl || !e2eStatusEl) return;
  let endpoint = "";
  try {
    endpoint = buildE2ELogsEndpoint();
  } catch (err) {
    e2eStatusEl.textContent = String(err.message || err);
    e2eLogEl.textContent = "";
    addActivity({ method: "UI", endpoint: "run/logs/validate", status: 400, message: String(err.message || err) });
    return;
  }
  e2eStatusEl.textContent = "Loading...";
  e2eLogEl.textContent = "Fetching logs...";
  const res = await fetch(endpoint);
  const contentType = res.headers.get("content-type") || "";
  if (contentType.includes("application/json")) {
    const body = await res.json();
    if (res.ok) {
      e2eStatusEl.textContent = `OK (${body.count || 0} events)`;
      e2eLogEl.textContent = JSON.stringify(body, null, 2);
    } else {
      e2eStatusEl.textContent = `Error (${res.status})`;
      e2eLogEl.textContent = JSON.stringify(body, null, 2);
    }
  } else {
    const text = await res.text();
    if (res.ok) {
      e2eStatusEl.textContent = "OK";
      e2eLogEl.textContent = text || "(empty)";
    } else {
      e2eStatusEl.textContent = `Error (${res.status})`;
      e2eLogEl.textContent = text || "request failed";
    }
  }
  addActivity({
    method: "GET",
    endpoint: "/run/logs",
    status: res.status,
    message: res.ok ? "End-to-end logs fetched" : "End-to-end logs failed"
  });
  e2eLogEl.scrollTop = e2eLogEl.scrollHeight;
}

function appendLogDelta(newText) {
  if (!runLogEl) return;
  runLogBuffer += newText;
  renderLogBuffer();
}

function stopRunStream() {
  if (runStreamController) {
    runStreamController.abort();
    runStreamController = null;
  }
}

function escapeHtml(str) {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function renderLogBuffer() {
  if (!runLogEl) return;
  const maxChars = 200000;
  if (runLogBuffer.length > maxChars) {
    runLogBuffer = runLogBuffer.slice(runLogBuffer.length - maxChars);
  }
  const lines = runLogBuffer.split("\n");
  const rendered = lines.map((line) => {
    const safe = escapeHtml(line);
    const isError = /(\berror\b|\bfailed\b|\bexception\b|\bpanic\b)/i.test(line);
    const isStep = /^(==>|---|###|\[step|\[task|\[pipeline|step |taskrun|starting|finished|completed|succeeded|failed)/i.test(line);
    const cls = isError ? "log-line error" : isStep ? "log-line step" : "log-line";
    return `<span class="${cls}">${safe}</span>`;
  }).join("\n");
  runLogEl.innerHTML = rendered;
  runLogEl.scrollTop = runLogEl.scrollHeight;
}

function stopPodLogStream() {
  if (podLogController) {
    podLogController.abort();
    podLogController = null;
  }
  podLogState.active = false;
}

async function startPodLogStream(workspace, pod, preEl, tail = "500", preserveBuffer = false) {
  if (!workspace || !pod || !preEl) return;
  const key = `${workspace}:${pod}:${tail}`;
  podLogTargetEl = preEl;
  if (podLogKey === key && podLogController) {
    preEl.textContent = podLogBuffer || "Pod log stream running...";
    preEl.scrollTop = preEl.scrollHeight;
    return;
  }
  stopPodLogStream();
  podLogKey = key;
  podLogState = { workspace, pod, tail, active: true };
  if (!preserveBuffer) {
    podLogBuffer = "";
  }
  podLogTargetEl.textContent = preserveBuffer ? podLogBuffer : "Pod log stream connecting...";
  podLogController = new AbortController();
  try {
    const res = await fetch(`/pod/logs/stream?workspace=${encodeURIComponent(workspace)}&pod=${encodeURIComponent(pod)}&tail=${encodeURIComponent(tail)}`, {
      signal: podLogController.signal
    });
    if (!res.ok || !res.body) {
      const msg = await res.text();
      if (podLogTargetEl) {
        podLogTargetEl.textContent = msg || `Pod log stream error (${res.status})`;
      }
      return;
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      const chunk = decoder.decode(value, { stream: true });
      if (chunk) {
        podLogBuffer += chunk;
        if (podLogBuffer.length > 200000) {
          podLogBuffer = podLogBuffer.slice(podLogBuffer.length - 200000);
        }
        if (podLogTargetEl) {
          podLogTargetEl.textContent = podLogBuffer;
          podLogTargetEl.scrollTop = podLogTargetEl.scrollHeight;
        }
      }
    }
  } catch (err) {
    if (err?.name !== "AbortError") {
      if (podLogTargetEl) {
        podLogTargetEl.textContent = `Pod log stream error: ${err}`;
      }
    }
  }
}

async function startRunStream() {
  if (!runState.name || !runLogEl) return;
  stopRunStream();
  const ns = runTaskNsEl?.value?.trim() || runState.namespace || "tekton-pipelines";
  const tail = runTaskTailEl?.value?.trim() || "2000";
  runLogBuffer = "";
  runLogEl.textContent = "Log stream connecting...";
  runStreamController = new AbortController();
  try {
    const res = await fetch(`/taskrun/logs/stream?name=${encodeURIComponent(runState.name)}&namespace=${encodeURIComponent(ns)}&tail=${encodeURIComponent(tail)}`, {
      signal: runStreamController.signal
    });
    if (!res.ok || !res.body) {
      const msg = await res.text();
      runLogEl.textContent = msg || `Log stream error (${res.status})`;
      return;
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      const chunk = decoder.decode(value, { stream: true });
      if (chunk) {
        appendLogDelta(chunk);
      }
    }
  } catch (err) {
    if (err?.name !== "AbortError") {
      runLogEl.textContent = `Log stream error: ${err}`;
    }
  }
}

async function refreshWorkspaceMetrics(ws, quiet = false) {
  if (!ws || !ws.startsWith("ws-") || wsMetricsInFlight[ws]) return;
  wsMetricsInFlight[ws] = true;
  const res = await handleRequest("GET", `/workspace/metrics?workspace=${encodeURIComponent(ws)}`, {}, quiet);
  wsMetricsCache[ws] = {
    ok: res.status === 200,
    body: res.body,
    status: res.status,
    fetchedAt: new Date().toLocaleTimeString()
  };
  wsMetricsInFlight[ws] = false;
  renderWorkspaceDetail();
}


async function healthCheck() {
  try {
    const res = await api("/healthz");
    healthBadge.textContent = `HEALTH: ${res.status}`;
    healthBadge.style.color = res.status === 200 ? "#37d6a5" : "#ff6d6d";
  } catch (err) {
    healthBadge.textContent = "HEALTH: ERR";
    healthBadge.style.color = "#ff6d6d";
  }
}

async function loadHostInfo() {
  const res = await api("/hostinfo");
  if (res.status === 200 && res.body?.host_ip) {
    hostInfo = res.body;
  }
}

async function loadExternalMap() {
  const res = await api("/external-map");
  if (res.status === 200 && Array.isArray(res.body)) {
    externalMap = res.body;
  }
}

function getExternalPort(ws, app) {
  const entry = externalMap.find((e) => e.workspace === ws && e.app === app);
  return entry?.external_port || null;
}

function isPortUsedByOther(port, ws, app) {
  return externalMap.some((e) => e.external_port === port && (e.workspace !== ws || e.app !== app));
}

async function setExternalPort(ws, app, port) {
  if (isPortUsedByOther(port, ws, app)) {
    addActivity({ method: "POST", endpoint: "/external-map", status: 409, message: "Port already in use" });
    return false;
  }
  const res = await handleRequest("POST", "/external-map", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ workspace: ws, app, external_port: port })
  }, true);
  if (res.status === 200) {
    await loadExternalMap();
    renderWorkspaceDetail();
    return true;
  }
  return false;
}

function defaultExternalPort(nodePort) {
  if (!nodePort) return null;
  return 18000 + (nodePort % 1000);
}

function stopWorkspacePoll() {
  if (wsPollTimer) {
    clearInterval(wsPollTimer);
    wsPollTimer = null;
  }
}

function startWorkspacePoll(ws) {
  stopWorkspacePoll();
  if (!ws || !ws.startsWith("ws-")) return;
  wsPollTimer = setInterval(() => {
    refreshWorkspaceStatus(ws, true);
  }, 5000);
}

function setCurrentWorkspace(ws) {
  currentWorkspace = ws;
  currentApp = null;
  syncE2EInputs();
  renderWorkspaceDetail();
  refreshWorkspaceStatus(ws, true);
  startWorkspacePoll(ws);
  prefetchEndpoints(ws);
}

function setCurrentApp(app) {
  currentApp = app;
  syncE2EInputs();
  renderWorkspaceDetail();
}

function renderWorkspaceCard(entry) {
  const card = document.createElement("div");
  card.className = "workspace-card";

  const header = document.createElement("div");
  header.className = "workspace-head";

  const title = document.createElement("div");
  title.className = "workspace-title";
  title.textContent = entry.workspace || "(unknown)";

  const actions = document.createElement("div");
  actions.className = "workspace-actions";

  const openBtn = document.createElement("button");
  openBtn.className = "btn ghost";
  openBtn.textContent = "Open";
  openBtn.onclick = () => setCurrentWorkspace(entry.workspace);

  actions.append(openBtn);
  header.append(title, actions);

  const apps = Array.isArray(entry.apps) ? entry.apps : [];
  const appList = document.createElement("div");
  appList.className = "app-list";
  if (apps.length === 0) {
    const empty = document.createElement("div");
    empty.className = "app-empty";
    empty.textContent = "Uygulama bulunamadi";
    appList.append(empty);
  } else {
    apps.forEach((app) => {
      const chip = document.createElement("button");
      chip.className = "app-chip";
      chip.textContent = app.app || app.name || "app";
      chip.onclick = () => {
        setCurrentWorkspace(entry.workspace);
        setCurrentApp(app.app || app.name || "app");
      };
      appList.append(chip);
    });
  }

  card.append(header, appList);
  return card;
}

async function refreshWorkspaces() {
  workspaceListEl.innerHTML = "";
  const res = await api("/workspaces");
  if (res.status !== 200) {
    const err = document.createElement("div");
    err.className = "workspace-empty";
    err.textContent = `Workspaces alinmadi (status ${res.status})`;
    workspaceListEl.append(err);
    addActivity({ method: "GET", endpoint: "/workspaces", status: res.status, message: toMessage(res.body) });
    return;
  }

  workspacesCache = Array.isArray(res.body) ? res.body : res.body?.items || [];
  if (!workspacesCache.length) {
    const empty = document.createElement("div");
    empty.className = "workspace-empty";
    empty.textContent = "Workspace bulunamadi.";
    workspaceListEl.append(empty);
    return;
  }

  workspacesCache.forEach((entry) => {
    workspaceListEl.append(renderWorkspaceCard(entry));
  });

  await refreshWorkspaceStatus(currentWorkspace, true);
}

async function refreshWorkspaceStatus(ws, quiet = false) {
  if (!ws || !ws.startsWith("ws-")) return;
  const res = await handleRequest("GET", `/workspace/status?workspace=${encodeURIComponent(ws)}`, {}, quiet);
  wsStatusCache[ws] = {
    ok: res.status === 200,
    body: res.body,
    status: res.status,
    fetchedAt: new Date().toLocaleTimeString()
  };
  refreshWorkspaceMetrics(ws, true);
  renderWorkspaceDetail();
}

function getPodCounts(status, appName) {
  const pods = Array.isArray(status?.pods) ? status.pods : [];
  const appPods = pods.filter((p) => p.name && p.name.startsWith(`${appName}-`));
  const running = appPods.filter((p) => p.phase === "Running").length;
  return { total: appPods.length, running };
}

function getServicePort(status, appName) {
  const services = Array.isArray(status?.services) ? status.services : [];
  const svc = services.find((s) => s.name === appName);
  return svc?.nodePort || null;
}

function endpointKey(ws, app) {
  return `${ws}::${app}`;
}

async function ensureEndpoint(ws, app) {
  const key = endpointKey(ws, app);
  if (endpointCache[key] || endpointInFlight[key]) return;
  endpointInFlight[key] = true;
  const res = await handleRequest("GET", `/endpoint?workspace=${encodeURIComponent(ws)}&app=${encodeURIComponent(app)}`, {}, true);
  if (res.status === 200 && res.body?.endpoint) {
    endpointCache[key] = {
      endpoint: res.body.endpoint,
      access: res.body?.access || null,
    };
    renderWorkspaceDetail();
  }
  delete endpointInFlight[key];
}

function formatAccessInfo(access) {
  if (!access || typeof access !== "object") return "-";
  const engine = access.engine || "-";
  const username = access.username ?? "";
  const password = access.password ?? "";
  const database = access.database ?? "";
  return `engine=${engine} user=${username} pass=${password} db=${database}`;
}

function prefetchEndpoints(ws) {
  const entry = workspacesCache.find((w) => w.workspace === ws);
  const apps = Array.isArray(entry?.apps) ? entry.apps : [];
  apps.forEach((a) => ensureEndpoint(ws, a.app || a.name || "app"));
}

function deriveApps(entry, status) {
  const apps = Array.isArray(entry?.apps) ? entry.apps : [];
  if (apps.length) return apps.map((a) => ({ app: a.app || a.name || "app" }));
  const derived = new Set();
  const services = Array.isArray(status?.services) ? status.services : [];
  services.forEach((s) => derived.add(s.name));
  const pods = Array.isArray(status?.pods) ? status.pods : [];
  pods.forEach((p) => {
    if (p.name && p.name.includes("-")) {
      derived.add(p.name.split("-").slice(0, -1).join("-"));
    }
  });
  return Array.from(derived).map((a) => ({ app: a }));
}

function renderWorkspaceDetail() {
  workspaceDetailEl.innerHTML = "";
  if (!currentWorkspace) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "Bir workspace secin.";
    workspaceDetailEl.append(empty);
    return;
  }

  const entry = workspacesCache.find((w) => w.workspace === currentWorkspace) || { workspace: currentWorkspace, apps: [] };
  const statusEntry = wsStatusCache[currentWorkspace] || {};
  const status = statusEntry.body || {};
  const apps = deriveApps(entry, status);

  const info = document.createElement("div");
  info.className = "detail-grid";

  const nameCard = document.createElement("div");
  nameCard.className = "detail-card";
  nameCard.innerHTML = `<div class="detail-label">Workspace</div><div class="detail-value">${currentWorkspace}</div>`;

  const appCount = document.createElement("div");
  appCount.className = "detail-card";
  appCount.innerHTML = `<div class="detail-label">Apps</div><div class="detail-value">${apps.length}</div>`;

  const statusText = statusEntry.fetchedAt ? (statusEntry.ok ? "OK" : "FAIL") : "PENDING";
  const statusCard = document.createElement("div");
  statusCard.className = "detail-card";
  statusCard.innerHTML = `<div class="detail-label">Status</div><div class="detail-value">${statusText}</div>
    <div class="detail-label">Last Update</div><div class="detail-value">${statusEntry.fetchedAt || "-"}</div>`;

  info.append(nameCard, appCount, statusCard);

  const actions = document.createElement("div");
  actions.className = "detail-actions";

  const restartBtn = document.createElement("button");
  restartBtn.className = "btn ghost";
  restartBtn.textContent = "Restart Workspace";
  restartBtn.onclick = async () => {
    await handleRequest("POST", `/workspace/restart?workspace=${encodeURIComponent(currentWorkspace)}`, { method: "POST" });
  };

  const deleteBtn = document.createElement("button");
  deleteBtn.className = "btn danger";
  deleteBtn.textContent = "Delete Workspace";
  deleteBtn.onclick = async () => {
    await handleRequest("POST", `/workspace/delete?workspace=${encodeURIComponent(currentWorkspace)}`, { method: "POST" });
    await refreshWorkspaces();
    currentWorkspace = null;
    stopWorkspacePoll();
    renderWorkspaceDetail();
  };

  actions.append(restartBtn, deleteBtn);

  const metricsBtn = document.createElement("button");
  metricsBtn.className = "btn ghost";
  metricsBtn.textContent = "Refresh Metrics";
  metricsBtn.onclick = async () => {
    await refreshWorkspaceMetrics(currentWorkspace);
  };
  actions.append(metricsBtn);

  const appSection = document.createElement("div");
  appSection.className = "detail-grid";

  apps.forEach((app) => {
    const name = app.app || app.name || "app";
    const counts = getPodCounts(status, name);
    const nodePort = getServicePort(status, name);
    const epKey = endpointKey(currentWorkspace, name);
    const endpointInfo = endpointCache[epKey] || null;
    const endpoint = endpointInfo?.endpoint || "loading...";
    const accessInfo = formatAccessInfo(endpointInfo?.access);

    const defaultPort = defaultExternalPort(nodePort);
    const mappedPort = getExternalPort(currentWorkspace, name);
    const draftKey = endpointKey(currentWorkspace, name);
    const draftVal = externalDraft[draftKey];
    const externalPort = draftVal !== undefined ? draftVal : (mappedPort || defaultPort);

    if (defaultPort && !mappedPort && externalDraft[draftKey] === undefined) {
      if (!externalInFlight[draftKey]) {
        externalInFlight[draftKey] = true;
        setExternalPort(currentWorkspace, name, defaultPort).finally(() => {
          externalInFlight[draftKey] = false;
        });
      }
    }

    const externalUrl = externalPort && hostInfo.host_ip ? `http://${hostInfo.host_ip}:${externalPort}` : "-";

    ensureEndpoint(currentWorkspace, name);

    const card = document.createElement("div");
    card.className = "detail-card";
    card.innerHTML = `<div class="detail-label">App</div><div class="detail-value">${name}</div>
      <div class="detail-label">Pods</div><div class="detail-value">${counts.running}/${counts.total}</div>
      <div class="detail-label">NodePort</div><div class="detail-value">${nodePort || "-"}</div>
      <div class="detail-label">Endpoint</div><div class="detail-value">${endpoint}</div>
      <div class="detail-label">External URL</div><div class="detail-value">${externalUrl}</div>
      <div class="detail-label">Access</div><div class="detail-value">${accessInfo}</div>`;

    const btns = document.createElement("div");
    btns.className = "detail-actions";

    const restartAppBtn = document.createElement("button");
    restartAppBtn.className = "btn ghost";
    restartAppBtn.textContent = "Restart";
    restartAppBtn.onclick = async () => {
      await handleRequest("POST", `/app/restart?workspace=${encodeURIComponent(currentWorkspace)}&app=${encodeURIComponent(name)}`, { method: "POST" });
    };

    const deleteAppBtn = document.createElement("button");
    deleteAppBtn.className = "btn danger";
    deleteAppBtn.textContent = "Delete";
    deleteAppBtn.onclick = async () => {
      await handleRequest("POST", `/app/delete?workspace=${encodeURIComponent(currentWorkspace)}&app=${encodeURIComponent(name)}`, { method: "POST" });
      await refreshWorkspaces();
      renderWorkspaceDetail();
    };

    const scaleWrap = document.createElement("div");
    scaleWrap.className = "detail-actions";
    const scaleInput = document.createElement("input");
    scaleInput.type = "number";
    scaleInput.min = "1";
    scaleInput.value = counts.total ? counts.total : "1";
    scaleInput.className = "scale-input";
    const scaleBtn = document.createElement("button");
    scaleBtn.className = "btn ghost";
    scaleBtn.textContent = "Scale";
    scaleBtn.onclick = async () => {
      await handleRequest("POST", `/workspace/scale?workspace=${encodeURIComponent(currentWorkspace)}&app=${encodeURIComponent(name)}&replicas=${encodeURIComponent(scaleInput.value)}`, { method: "POST" });
      await refreshWorkspaceStatus(currentWorkspace, true);
    };
    scaleWrap.append(scaleInput, scaleBtn);

    const externalWrap = document.createElement("div");
    externalWrap.className = "detail-actions";
    const externalInput = document.createElement("input");
    externalInput.type = "number";
    externalInput.min = "1";
    externalInput.placeholder = "External port";
    externalInput.value = externalPort || "";
    externalInput.className = "external-input";
    externalInput.oninput = () => {
      externalDraft[draftKey] = parseInt(externalInput.value, 10) || "";
    };
    const externalBtn = document.createElement("button");
    externalBtn.className = "btn ghost";
    externalBtn.textContent = "Set External";
    externalBtn.onclick = async () => {
      const port = parseInt(externalInput.value, 10);
      if (!port) return;
      const ok = await setExternalPort(currentWorkspace, name, port);
      if (ok) {
        delete externalDraft[draftKey];
        await loadExternalMap();
        renderWorkspaceDetail();
      }
    };
    externalWrap.append(externalInput, externalBtn);

    btns.append(restartAppBtn, deleteAppBtn);

    card.append(btns, scaleWrap, externalWrap);
    appSection.append(card);
  });

  const metricsEntry = wsMetricsCache[currentWorkspace] || {};
  const metrics = metricsEntry.body || {};
  const metricsCard = document.createElement("div");
  metricsCard.className = "detail-card metric-wide";
  const cpuAlloc = metrics?.node?.allocatable_mcpu ?? "-";
  const memAlloc = metrics?.node?.allocatable_mem_mi ?? "-";
  const cpuUse = metrics?.usage?.pods_cpu_mcpu ?? "-";
  const memUse = metrics?.usage?.pods_mem_mi ?? "-";
  const cpuReq = metrics?.requests?.cpu_mcpu ?? "-";
  const memReq = metrics?.requests?.mem_mi ?? "-";
  const diskTotal = metrics?.disk?.workspace_total ?? "-";
  const diskTotalBytes = metrics?.disk?.workspace_total_bytes ?? 0;
  const diskCapacityRaw = metrics?.disk?.size ?? "";
  const parseSize = (val) => {
    if (!val || typeof val !== "string") return 0;
    const m = val.trim().match(/^([\d.]+)\s*([KMGTP]?)B?$/i);
    if (!m) return 0;
    const num = parseFloat(m[1]);
    const unit = m[2].toUpperCase();
    const mult = unit === "K" ? 1024 : unit === "M" ? 1024 ** 2 : unit === "G" ? 1024 ** 3 : unit === "T" ? 1024 ** 4 : unit === "P" ? 1024 ** 5 : 1;
    return Math.round(num * mult);
  };
  const diskCapacityBytes = parseSize(diskCapacityRaw);
  const diskWorkspacePct = diskCapacityBytes ? Math.min(100, Math.round((diskTotalBytes / diskCapacityBytes) * 100)) : 0;
  const cpuRemain = (typeof cpuAlloc === "number" && typeof cpuUse === "number") ? Math.max(cpuAlloc - cpuUse, 0) : "-";
  const memRemain = (typeof memAlloc === "number" && typeof memUse === "number") ? Math.max(memAlloc - memUse, 0) : "-";
  const metricsErr = metricsEntry.status && metricsEntry.status !== 200 ? `Metrics error (${metricsEntry.status})` : "";
  const nodeErr = metrics?.errors?.node_metrics ? `node: ${metrics.errors.node_metrics}` : "";
  const podErr = metrics?.errors?.pod_metrics ? `pods: ${metrics.errors.pod_metrics}` : "";
  const errLine = [metricsErr, nodeErr, podErr].filter(Boolean).join(" | ");
  const cpuPct = (typeof cpuAlloc === "number" && typeof cpuUse === "number" && cpuAlloc > 0) ? Math.min(100, Math.round((cpuUse / cpuAlloc) * 100)) : 0;
  const memPct = (typeof memAlloc === "number" && typeof memUse === "number" && memAlloc > 0) ? Math.min(100, Math.round((memUse / memAlloc) * 100)) : 0;
  metricsCard.innerHTML = `<div class="detail-label">Metrics</div><div class="detail-value">${metricsEntry.fetchedAt || "-"}</div>
    <div class="detail-label">CPU (alloc/workspace/req/left mCPU)</div><div class="detail-value">${cpuAlloc} / ${cpuUse} / ${cpuReq} / ${cpuRemain}</div>
    <div class="metric-bar"><div class="metric-bar-fill" style="width:${cpuPct}%"></div></div>
    <div class="detail-label">Memory (alloc/workspace/req/left Mi)</div><div class="detail-value">${memAlloc} / ${memUse} / ${memReq} / ${memRemain}</div>
    <div class="metric-bar"><div class="metric-bar-fill memory" style="width:${memPct}%"></div></div>
    <div class="detail-label">Disk Usage (workspace)</div><div class="detail-value">${diskTotal}</div>
    <div class="metric-bar"><div class="metric-bar-fill disk" style="width:${diskWorkspacePct}%"></div></div>
    ${errLine ? `<div class="detail-label">Errors</div><div class="detail-value">${errLine}</div>` : ""}`;

  const pods = Array.isArray(metrics?.pods) ? metrics.pods : [];
  const byApp = {};
  pods.forEach((p) => {
    const name = p.name || "";
    const base = name.includes("-") ? name.split("-").slice(0, -1).join("-") : name;
    if (!base) return;
    if (!byApp[base]) {
      byApp[base] = { app: base, cpu_mcpu: 0, mem_mi: 0, pods: 0 };
    }
    byApp[base].pods += 1;
    byApp[base].cpu_mcpu += p.cpu_mcpu || 0;
    byApp[base].mem_mi += p.mem_mi || 0;
  });
  const appRows = Object.values(byApp);
  if (appRows.length) {
    const table = document.createElement("div");
    table.className = "metric-table";
    const head = document.createElement("div");
    head.className = "metric-row head";
    head.innerHTML = `<div>App</div><div>Pods</div><div>CPU mCPU</div><div>Mem Mi</div>`;
    table.append(head);
    appRows.sort((a, b) => b.cpu_mcpu - a.cpu_mcpu);
    appRows.forEach((row) => {
      const r = document.createElement("div");
      r.className = "metric-row";
      const cpuBarPct = (typeof cpuAlloc === "number" && cpuAlloc > 0) ? Math.min(100, Math.round((row.cpu_mcpu / cpuAlloc) * 100)) : 0;
      const memBarPct = (typeof memAlloc === "number" && memAlloc > 0) ? Math.min(100, Math.round((row.mem_mi / memAlloc) * 100)) : 0;
      r.innerHTML = `<div>${row.app}</div><div>${row.pods}</div><div>${row.cpu_mcpu}</div><div>${row.mem_mi}</div>
        <div class="metric-row-bars">
          <div class="metric-bar-wrap">
            <div class="metric-bar-label">CPU</div>
            <div class="metric-bar"><div class="metric-bar-fill" style="width:${cpuBarPct}%"></div></div>
          </div>
          <div class="metric-bar-wrap">
            <div class="metric-bar-label">Memory</div>
            <div class="metric-bar"><div class="metric-bar-fill memory" style="width:${memBarPct}%"></div></div>
          </div>
        </div>`;
      table.append(r);
    });
    metricsCard.append(table);
  }

  const logCard = document.createElement("div");
  logCard.className = "detail-card metric-wide";
  const podsForSelect = Array.isArray(status?.pods) ? status.pods : [];
  const options = podsForSelect.map((p) => `<option value="${p.name}">${p.name}</option>`).join("");
  logCard.innerHTML = `<div class="detail-label">Pod Logs</div>
    <div class="detail-value">Workspace: ${currentWorkspace}</div>
    <div class="log-controls">
      <select class="log-select" id="podSelect">${options || "<option value=\"\">No pods</option>"}</select>
      <input class="log-tail" id="podTail" type="number" min="50" value="500" />
      <button class="btn ghost" id="podLogStart" type="button">Start</button>
      <button class="btn ghost" id="podLogStop" type="button">Stop</button>
    </div>
    <pre class="log-box" id="podLogBox"></pre>`;

  workspaceDetailEl.append(info, actions, metricsCard, logCard, appSection);

  const podSelect = logCard.querySelector("#podSelect");
  const podTail = logCard.querySelector("#podTail");
  const podLogBox = logCard.querySelector("#podLogBox");
  const startBtn = logCard.querySelector("#podLogStart");
  const stopBtn = logCard.querySelector("#podLogStop");
  if (podLogState.workspace === currentWorkspace && podSelect && podLogState.pod) {
    podSelect.value = podLogState.pod;
  }
  if (podTail && podLogState.tail) {
    podTail.value = podLogState.tail;
  }
  if (podLogState.active && podLogState.workspace === currentWorkspace && podLogState.pod) {
    startPodLogStream(currentWorkspace, podLogState.pod, podLogBox, podLogState.tail, true);
  }
  if (startBtn) {
    startBtn.onclick = () => {
      const podName = podSelect?.value || "";
      const tail = podTail?.value || "500";
      if (!podName) {
        podLogBox.textContent = "Pod secilmedi.";
        return;
      }
      startPodLogStream(currentWorkspace, podName, podLogBox, tail);
    };
  }
  if (stopBtn) {
    stopBtn.onclick = () => {
      stopPodLogStream();
      if (podLogBox) podLogBox.textContent = "Stopped.";
    };
  }
}

function buildGitPayload() {
  const appName = document.getElementById("gitAppName").value.trim();
  const workspace = document.getElementById("gitWorkspace").value.trim();
  const repoUrl = document.getElementById("gitRepoUrl").value.trim();
  const revision = document.getElementById("gitRevision").value.trim();
  const gitUser = document.getElementById("gitUser").value.trim();
  const gitToken = document.getElementById("gitToken").value.trim();
  const project = document.getElementById("gitImageProject").value.trim();
  const tag = document.getElementById("gitImageTag").value.trim();
  const registry = document.getElementById("gitRegistry").value.trim();
  const port = parseInt(document.getElementById("gitPort").value, 10) || 3000;
  const depType = document.getElementById("gitDepType").value;
  const migrationEnabled = document.getElementById("gitMigrationEnabled").value === "true";

  const source = { type: "git", repo_url: repoUrl, revision: revision || "main" };
  if (gitUser) source.git_username = gitUser;
  if (gitToken) source.git_token = gitToken;

  const payload = {
    app_name: appName,
    workspace: workspace,
    source,
    image: {
      project: project || appName,
      tag: tag || "latest",
      registry: registry || "lenovo:8443"
    },
    deploy: { container_port: port }
  };

  if (depType !== "none") {
    payload.dependency = { type: depType };
    if (depType === "redis" || depType === "both") {
      payload.dependency.redis = {
        image: document.getElementById("gitRedisImage").value.trim() || "lenovo:8443/library/redis:7-alpine",
        service_name: document.getElementById("gitRedisService").value.trim() || "redis",
        port: parseInt(document.getElementById("gitRedisPort").value, 10) || 6379,
        connection_env: document.getElementById("gitRedisEnv").value.trim() || "ConnectionStrings__Redis"
      };
    }
    if (depType === "sql" || depType === "both") {
      const sqlDb = document.getElementById("gitSqlDb").value.trim();
      const sqlPass = document.getElementById("gitSqlPass").value.trim();
      if (!sqlDb || !sqlPass) {
        throw new Error("SQL dependency icin SQL Database ve SQL Password zorunlu.");
      }
      payload.dependency.sql = {
        image: document.getElementById("gitSqlImage").value.trim() || "lenovo:8443/library/postgres:16-alpine",
        service_name: document.getElementById("gitSqlService").value.trim() || "postgres",
        port: parseInt(document.getElementById("gitSqlPort").value, 10) || 5432,
        database: sqlDb,
        username: document.getElementById("gitSqlUser").value.trim() || "postgres",
        password: sqlPass,
        connection_env: document.getElementById("gitSqlEnv").value.trim() || "ConnectionStrings__DefaultConnection"
      };
    }
  }

  if (migrationEnabled) {
    const commandCsv = document.getElementById("gitMigrationCmd").value.trim();
    const argsCsv = document.getElementById("gitMigrationArgs").value.trim();
    if (!commandCsv && argsCsv) {
      throw new Error("Migration Args verildiyse Migration Command da girilmeli.");
    }
    payload.migration = {
      enabled: true,
      image: document.getElementById("gitMigrationImage").value.trim(),
      command: commandCsv ? commandCsv.split(",").map((s) => s.trim()).filter(Boolean) : [],
      args: argsCsv ? argsCsv.split(",").map((s) => s.trim()).filter(Boolean) : [],
      env_name: document.getElementById("gitMigrationEnv").value.trim() || "ConnectionStrings__DefaultConnection"
    };
  }
  return payload;
}

function updateGitDependencyVisibility() {
  const depType = document.getElementById("gitDepType")?.value || "none";
  const migrationEnabled = document.getElementById("gitMigrationEnabled")?.value === "true";
  const showSql = depType === "sql" || depType === "both";

  const sqlDbWrap = document.getElementById("gitSqlDbWrap");
  const sqlPassWrap = document.getElementById("gitSqlPassWrap");
  const migrationCmdWrap = document.getElementById("gitMigrationCmdWrap");
  const migrationArgsWrap = document.getElementById("gitMigrationArgsWrap");

  if (sqlDbWrap) sqlDbWrap.style.display = showSql ? "" : "none";
  if (sqlPassWrap) sqlPassWrap.style.display = showSql ? "" : "none";
  if (migrationCmdWrap) migrationCmdWrap.style.display = migrationEnabled ? "" : "none";
  if (migrationArgsWrap) migrationArgsWrap.style.display = migrationEnabled ? "" : "none";

  if (!showSql && migrationEnabled) {
    const migrationSelect = document.getElementById("gitMigrationEnabled");
    if (migrationSelect) migrationSelect.value = "false";
    if (migrationCmdWrap) migrationCmdWrap.style.display = "none";
    if (migrationArgsWrap) migrationArgsWrap.style.display = "none";
    addActivity({ method: "UI", endpoint: "migration", status: 400, message: "Migration sadece sql/both dependency ile kullanilabilir." });
  }
}

function buildDependencyPayload(prefix) {
  const depType = document.getElementById(`${prefix}DepType`).value;
  if (depType === "none") {
    return null;
  }

  const dep = { type: depType };
  if (depType === "redis" || depType === "both") {
    dep.redis = {
      image: document.getElementById(`${prefix}RedisImage`).value.trim() || "lenovo:8443/library/redis:7-alpine",
      service_name: document.getElementById(`${prefix}RedisService`).value.trim() || "redis",
      port: parseInt(document.getElementById(`${prefix}RedisPort`).value, 10) || 6379,
      connection_env: document.getElementById(`${prefix}RedisEnv`).value.trim() || "ConnectionStrings__Redis"
    };
  }
  if (depType === "sql" || depType === "both") {
    const sqlDb = document.getElementById(`${prefix}SqlDb`).value.trim();
    const sqlPass = document.getElementById(`${prefix}SqlPass`).value.trim();
    if (!sqlDb || !sqlPass) {
      throw new Error("SQL dependency icin SQL Database ve SQL Password zorunlu.");
    }
    dep.sql = {
      image: document.getElementById(`${prefix}SqlImage`).value.trim() || "lenovo:8443/library/postgres:16-alpine",
      service_name: document.getElementById(`${prefix}SqlService`).value.trim() || "postgres",
      port: parseInt(document.getElementById(`${prefix}SqlPort`).value, 10) || 5432,
      database: sqlDb,
      username: document.getElementById(`${prefix}SqlUser`).value.trim() || "postgres",
      password: sqlPass,
      connection_env: document.getElementById(`${prefix}SqlEnv`).value.trim() || "ConnectionStrings__DefaultConnection"
    };
  }
  return dep;
}

function buildMigrationPayload(prefix, depType) {
  const migrationEnabled = document.getElementById(`${prefix}MigrationEnabled`)?.value === "true";
  if (!migrationEnabled) {
    return null;
  }
  if (depType !== "sql" && depType !== "both") {
    throw new Error("Migration sadece sql/both dependency ile kullanilabilir.");
  }
  const commandCsv = document.getElementById(`${prefix}MigrationCmd`)?.value.trim() || "";
  const argsCsv = document.getElementById(`${prefix}MigrationArgs`)?.value.trim() || "";
  if (!commandCsv && argsCsv) {
    throw new Error("Migration Args verildiyse Migration Command da girilmeli.");
  }
  return {
    enabled: true,
    image: document.getElementById(`${prefix}MigrationImage`)?.value.trim() || "",
    command: commandCsv ? commandCsv.split(",").map((s) => s.trim()).filter(Boolean) : [],
    args: argsCsv ? argsCsv.split(",").map((s) => s.trim()).filter(Boolean) : [],
    env_name: document.getElementById(`${prefix}MigrationEnv`)?.value.trim() || "ConnectionStrings__DefaultConnection"
  };
}

function updateDependencyVisibility(prefix) {
  const depType = document.getElementById(`${prefix}DepType`)?.value || "none";
  const migrationEnabled = document.getElementById(`${prefix}MigrationEnabled`)?.value === "true";
  const showSql = depType === "sql" || depType === "both";
  const sqlDbWrap = document.getElementById(`${prefix}SqlDbWrap`);
  const sqlPassWrap = document.getElementById(`${prefix}SqlPassWrap`);
  const migrationCmdWrap = document.getElementById(`${prefix}MigrationCmdWrap`);
  const migrationArgsWrap = document.getElementById(`${prefix}MigrationArgsWrap`);
  if (sqlDbWrap) sqlDbWrap.style.display = showSql ? "" : "none";
  if (sqlPassWrap) sqlPassWrap.style.display = showSql ? "" : "none";
  if (migrationCmdWrap) migrationCmdWrap.style.display = migrationEnabled ? "" : "none";
  if (migrationArgsWrap) migrationArgsWrap.style.display = migrationEnabled ? "" : "none";

  if (!showSql && migrationEnabled) {
    const migrationSelect = document.getElementById(`${prefix}MigrationEnabled`);
    if (migrationSelect) migrationSelect.value = "false";
    if (migrationCmdWrap) migrationCmdWrap.style.display = "none";
    if (migrationArgsWrap) migrationArgsWrap.style.display = "none";
    addActivity({ method: "UI", endpoint: "migration", status: 400, message: "Migration sadece sql/both dependency ile kullanilabilir." });
  }
}

function buildZipPayload() {
  const appName = document.getElementById("zipAppName").value.trim();
  const workspace = document.getElementById("zipWorkspace").value.trim();
  const zipUrl = document.getElementById("zipUrl").value.trim();
  const project = document.getElementById("zipImageProject").value.trim();
  const tag = document.getElementById("zipImageTag").value.trim();
  const registry = document.getElementById("zipRegistry").value.trim();
  const port = parseInt(document.getElementById("zipPort").value, 10) || 8080;

  const payload = {
    app_name: appName,
    workspace: workspace,
    source: { type: "zip", zip_url: zipUrl },
    image: {
      project: project || appName,
      tag: tag || "latest",
      registry: registry || "lenovo:8443"
    },
    deploy: { container_port: port }
  };
  const dep = buildDependencyPayload("zip");
  const depType = dep?.type || "none";
  if (dep) payload.dependency = dep;
  const migration = buildMigrationPayload("zip", depType);
  if (migration) payload.migration = migration;
  return payload;
}

function buildLocalPayload() {
  const appName = document.getElementById("localAppName").value.trim();
  const workspace = document.getElementById("localWorkspace").value.trim();
  const localPath = document.getElementById("localPath").value.trim();
  const project = document.getElementById("localImageProject").value.trim();
  const tag = document.getElementById("localImageTag").value.trim();
  const registry = document.getElementById("localRegistry").value.trim();
  const port = parseInt(document.getElementById("localPort").value, 10) || 3000;

  const payload = {
    app_name: appName,
    workspace: workspace,
    source: { type: "local", local_path: localPath },
    image: {
      project: project || appName,
      tag: tag || "latest",
      registry: registry || "lenovo:8443"
    },
    deploy: { container_port: port }
  };
  const dep = buildDependencyPayload("local");
  const depType = dep?.type || "none";
  if (dep) payload.dependency = dep;
  const migration = buildMigrationPayload("local", depType);
  if (migration) payload.migration = migration;
  return payload;
}

function fillGitForm(sample) {
  document.getElementById("gitAppName").value = sample.app_name;
  document.getElementById("gitWorkspace").value = sample.workspace;
  document.getElementById("gitRepoUrl").value = sample.source.repo_url || "";
  document.getElementById("gitRevision").value = sample.source.revision || "";
  document.getElementById("gitImageProject").value = sample.image.project;
  document.getElementById("gitImageTag").value = sample.image.tag;
  document.getElementById("gitRegistry").value = sample.image.registry;
  document.getElementById("gitPort").value = sample.deploy.container_port;
  const depType = sample.dependency?.type || "none";
  document.getElementById("gitDepType").value = depType;
  document.getElementById("gitRedisImage").value = sample.dependency?.redis?.image || "lenovo:8443/library/redis:7-alpine";
  document.getElementById("gitRedisService").value = sample.dependency?.redis?.service_name || "redis";
  document.getElementById("gitRedisPort").value = sample.dependency?.redis?.port || 6379;
  document.getElementById("gitRedisEnv").value = sample.dependency?.redis?.connection_env || "ConnectionStrings__Redis";
  document.getElementById("gitSqlImage").value = sample.dependency?.sql?.image || "lenovo:8443/library/postgres:16-alpine";
  document.getElementById("gitSqlService").value = sample.dependency?.sql?.service_name || "postgres";
  document.getElementById("gitSqlPort").value = sample.dependency?.sql?.port || 5432;
  document.getElementById("gitSqlDb").value = sample.dependency?.sql?.database || "AppDb";
  document.getElementById("gitSqlUser").value = sample.dependency?.sql?.username || "postgres";
  document.getElementById("gitSqlPass").value = sample.dependency?.sql?.password || "";
  document.getElementById("gitSqlEnv").value = sample.dependency?.sql?.connection_env || "ConnectionStrings__DefaultConnection";
  document.getElementById("gitMigrationEnabled").value = sample.migration?.enabled ? "true" : "false";
  document.getElementById("gitMigrationImage").value = sample.migration?.image || "";
  document.getElementById("gitMigrationCmd").value = Array.isArray(sample.migration?.command) ? sample.migration.command.join(",") : "";
  document.getElementById("gitMigrationArgs").value = Array.isArray(sample.migration?.args) ? sample.migration.args.join(",") : "";
  document.getElementById("gitMigrationEnv").value = sample.migration?.env_name || "ConnectionStrings__DefaultConnection";
  updateGitDependencyVisibility();
}

function fillZipForm(sample) {
  document.getElementById("zipAppName").value = sample.app_name;
  document.getElementById("zipWorkspace").value = sample.workspace;
  document.getElementById("zipUrl").value = sample.source.zip_url || "";
  document.getElementById("zipImageProject").value = sample.image.project;
  document.getElementById("zipImageTag").value = sample.image.tag;
  document.getElementById("zipRegistry").value = sample.image.registry;
  document.getElementById("zipPort").value = sample.deploy.container_port;
  const depType = sample.dependency?.type || "none";
  document.getElementById("zipDepType").value = depType;
  document.getElementById("zipRedisImage").value = sample.dependency?.redis?.image || "lenovo:8443/library/redis:7-alpine";
  document.getElementById("zipRedisService").value = sample.dependency?.redis?.service_name || "redis";
  document.getElementById("zipRedisPort").value = sample.dependency?.redis?.port || 6379;
  document.getElementById("zipRedisEnv").value = sample.dependency?.redis?.connection_env || "ConnectionStrings__Redis";
  document.getElementById("zipSqlImage").value = sample.dependency?.sql?.image || "lenovo:8443/library/postgres:16-alpine";
  document.getElementById("zipSqlService").value = sample.dependency?.sql?.service_name || "postgres";
  document.getElementById("zipSqlPort").value = sample.dependency?.sql?.port || 5432;
  document.getElementById("zipSqlDb").value = sample.dependency?.sql?.database || "AppDb";
  document.getElementById("zipSqlUser").value = sample.dependency?.sql?.username || "postgres";
  document.getElementById("zipSqlPass").value = sample.dependency?.sql?.password || "";
  document.getElementById("zipSqlEnv").value = sample.dependency?.sql?.connection_env || "ConnectionStrings__DefaultConnection";
  document.getElementById("zipMigrationEnabled").value = sample.migration?.enabled ? "true" : "false";
  document.getElementById("zipMigrationImage").value = sample.migration?.image || "";
  document.getElementById("zipMigrationCmd").value = Array.isArray(sample.migration?.command) ? sample.migration.command.join(",") : "";
  document.getElementById("zipMigrationArgs").value = Array.isArray(sample.migration?.args) ? sample.migration.args.join(",") : "";
  document.getElementById("zipMigrationEnv").value = sample.migration?.env_name || "ConnectionStrings__DefaultConnection";
  updateDependencyVisibility("zip");
}

function fillLocalForm(sample) {
  document.getElementById("localAppName").value = sample.app_name;
  document.getElementById("localWorkspace").value = sample.workspace;
  document.getElementById("localPath").value = sample.source.local_path || "";
  document.getElementById("localImageProject").value = sample.image.project;
  document.getElementById("localImageTag").value = sample.image.tag;
  document.getElementById("localRegistry").value = sample.image.registry;
  document.getElementById("localPort").value = sample.deploy.container_port;
  const depType = sample.dependency?.type || "none";
  document.getElementById("localDepType").value = depType;
  document.getElementById("localRedisImage").value = sample.dependency?.redis?.image || "lenovo:8443/library/redis:7-alpine";
  document.getElementById("localRedisService").value = sample.dependency?.redis?.service_name || "redis";
  document.getElementById("localRedisPort").value = sample.dependency?.redis?.port || 6379;
  document.getElementById("localRedisEnv").value = sample.dependency?.redis?.connection_env || "ConnectionStrings__Redis";
  document.getElementById("localSqlImage").value = sample.dependency?.sql?.image || "lenovo:8443/library/postgres:16-alpine";
  document.getElementById("localSqlService").value = sample.dependency?.sql?.service_name || "postgres";
  document.getElementById("localSqlPort").value = sample.dependency?.sql?.port || 5432;
  document.getElementById("localSqlDb").value = sample.dependency?.sql?.database || "AppDb";
  document.getElementById("localSqlUser").value = sample.dependency?.sql?.username || "postgres";
  document.getElementById("localSqlPass").value = sample.dependency?.sql?.password || "";
  document.getElementById("localSqlEnv").value = sample.dependency?.sql?.connection_env || "ConnectionStrings__DefaultConnection";
  document.getElementById("localMigrationEnabled").value = sample.migration?.enabled ? "true" : "false";
  document.getElementById("localMigrationImage").value = sample.migration?.image || "";
  document.getElementById("localMigrationCmd").value = Array.isArray(sample.migration?.command) ? sample.migration.command.join(",") : "";
  document.getElementById("localMigrationArgs").value = Array.isArray(sample.migration?.args) ? sample.migration.args.join(",") : "";
  document.getElementById("localMigrationEnv").value = sample.migration?.env_name || "ConnectionStrings__DefaultConnection";
  updateDependencyVisibility("local");
}

function closeSampleModal() {
  const el = document.getElementById("sampleModal");
  if (el) el.remove();
}

function showSampleModal(title, sample, fillFn) {
  closeSampleModal();
  const json = JSON.stringify(sample, null, 2);

  const backdrop = document.createElement("div");
  backdrop.id = "sampleModal";
  backdrop.className = "modal-backdrop";
  backdrop.innerHTML = `
    <div class="modal-card" role="dialog" aria-modal="true" aria-label="${title}">
      <div class="modal-head">
        <div class="modal-title">${title}</div>
        <button class="btn ghost" id="sampleModalClose" type="button">Close</button>
      </div>
      <div class="modal-sub">JSON ornegini kopyalayabilir veya forma otomatik doldurabilirsiniz.</div>
      <pre class="modal-json" id="sampleModalJson"></pre>
      <div class="modal-actions">
        <button class="btn ghost" id="sampleModalCopy" type="button">Copy JSON</button>
        <button class="btn accent" id="sampleModalFill" type="button">Fill Form</button>
      </div>
    </div>
  `;
  document.body.appendChild(backdrop);

  const pre = document.getElementById("sampleModalJson");
  if (pre) pre.textContent = json;

  const closeBtn = document.getElementById("sampleModalClose");
  if (closeBtn) closeBtn.onclick = closeSampleModal;

  const copyBtn = document.getElementById("sampleModalCopy");
  if (copyBtn) {
    copyBtn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(json);
        addActivity({ method: "UI", endpoint: "sample/json", status: 200, message: `${title} copied` });
      } catch (err) {
        addActivity({ method: "UI", endpoint: "sample/json", status: 500, message: `copy failed: ${err}` });
      }
    };
  }

  const fillBtn = document.getElementById("sampleModalFill");
  if (fillBtn) {
    fillBtn.onclick = () => {
      fillFn(sample);
      closeSampleModal();
      addActivity({ method: "UI", endpoint: "sample/fill", status: 200, message: `${title} applied to form` });
    };
  }

  backdrop.onclick = (e) => {
    if (e.target === backdrop) closeSampleModal();
  };
}

const sampleGit = {
  app_name: "demoapp",
  workspace: "ws-demo",
  source: {
    type: "git",
    repo_url: "https://github.com/mehmetalpkarabulut/Dev",
    revision: "main",
    git_username: "GITHUB_USER",
    git_token: "GITHUB_TOKEN"
  },
  image: {
    project: "demoapp",
    tag: "latest",
    registry: "lenovo:8443"
  },
  deploy: { container_port: 3000 },
  dependency: {
    type: "both",
    redis: {
      image: "lenovo:8443/library/redis:7-alpine",
      service_name: "redis",
      port: 6379,
      connection_env: "ConnectionStrings__Redis"
    },
    sql: {
      image: "lenovo:8443/library/postgres:16-alpine",
      service_name: "postgres",
      port: 5432,
      database: "AppDb",
      username: "postgres",
      password: "StrongPass_123!",
      connection_env: "ConnectionStrings__DefaultConnection"
    }
  },
  migration: {
    enabled: true,
    image: "",
    command: [],
    args: [],
    env_name: "ConnectionStrings__DefaultConnection"
  }
};

const sampleZip = {
  app_name: "demoapp",
  workspace: "ws-demo",
  source: {
    type: "zip",
    zip_url: "http://zip-server.tekton-pipelines.svc.cluster.local:8080/app.zip"
  },
  image: {
    project: "demoapp",
    tag: "latest",
    registry: "lenovo:8443"
  },
  deploy: { container_port: 8080 },
  dependency: {
    type: "both",
    redis: {
      image: "lenovo:8443/library/redis:7-alpine",
      service_name: "redis",
      port: 6379,
      connection_env: "ConnectionStrings__Redis"
    },
    sql: {
      image: "lenovo:8443/library/postgres:16-alpine",
      service_name: "postgres",
      port: 5432,
      database: "AppDb",
      username: "postgres",
      password: "StrongPass_123!",
      connection_env: "ConnectionStrings__DefaultConnection"
    }
  },
  migration: {
    enabled: true,
    image: "",
    command: [],
    args: [],
    env_name: "ConnectionStrings__DefaultConnection"
  }
};

const sampleLocal = {
  app_name: "demoapp",
  workspace: "ws-demo",
  source: {
    type: "local",
    local_path: "/mnt/projects/demoapp"
  },
  image: {
    project: "demoapp",
    tag: "latest",
    registry: "lenovo:8443"
  },
  deploy: { container_port: 3000 },
  dependency: {
    type: "both",
    redis: {
      image: "lenovo:8443/library/redis:7-alpine",
      service_name: "redis",
      port: 6379,
      connection_env: "ConnectionStrings__Redis"
    },
    sql: {
      image: "lenovo:8443/library/postgres:16-alpine",
      service_name: "postgres",
      port: 5432,
      database: "AppDb",
      username: "postgres",
      password: "StrongPass_123!",
      connection_env: "ConnectionStrings__DefaultConnection"
    }
  },
  migration: {
    enabled: true,
    image: "",
    command: [],
    args: [],
    env_name: "ConnectionStrings__DefaultConnection"
  }
};

document.getElementById("sampleGit").onclick = () => showSampleModal("Git Sample JSON", sampleGit, fillGitForm);
document.getElementById("sampleZip").onclick = () => showSampleModal("ZIP Sample JSON", sampleZip, fillZipForm);
document.getElementById("sampleLocal").onclick = () => showSampleModal("Local Sample JSON", sampleLocal, fillLocalForm);

document.getElementById("healthBtn").onclick = async () => {
  await handleRequest("GET", "/healthz");
  healthCheck();
};

async function submitRun(payload) {
  const res = await handleRequest("POST", "/run", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  });
  if (res.status === 202 && res.body?.taskrun) {
    lastRunId = res.body?.run_id || "";
    setRunContext(res.body.taskrun, res.body.namespace);
    if (e2eWorkspaceEl && res.body?.workspace) e2eWorkspaceEl.value = res.body.workspace;
    if (e2eAppEl && res.body?.app) e2eAppEl.value = res.body.app;
    if (e2eRunIdEl && res.body?.run_id) e2eRunIdEl.value = res.body.run_id;
    if (runLogEl) {
      runLogEl.textContent = "Loglar geliyor...";
      runLogEl.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }
  await refreshWorkspaces();
}

document.getElementById("runGitBtn").onclick = async () => {
  try {
    const payload = buildGitPayload();
    await submitRun(payload);
  } catch (err) {
    addActivity({ method: "UI", endpoint: "git/validate", status: 400, message: String(err.message || err) });
  }
};

document.getElementById("runZipBtn").onclick = async () => {
  try {
    const payload = buildZipPayload();
    await submitRun(payload);
  } catch (err) {
    addActivity({ method: "UI", endpoint: "zip/validate", status: 400, message: String(err.message || err) });
  }
};

document.getElementById("runLocalBtn").onclick = async () => {
  try {
    const payload = buildLocalPayload();
    await submitRun(payload);
  } catch (err) {
    addActivity({ method: "UI", endpoint: "local/validate", status: 400, message: String(err.message || err) });
  }
};

document.getElementById("wsRefresh").onclick = () => refreshWorkspaces();

document.getElementById("clearLog").onclick = () => {
  activityCache = [];
  renderActivity();
};

if (runTaskNameEl) {
  runTaskNameEl.onchange = () => {
    runState.name = runTaskNameEl.value.trim();
    startRunPoll();
    startRunStream();
  };
}
if (runTaskNsEl) {
  runTaskNsEl.onchange = () => {
    runState.namespace = runTaskNsEl.value.trim();
    startRunStream();
  };
}

document.getElementById("runRefresh").onclick = async () => {
  await refreshRunStatus();
  await refreshRunLogs();
};
document.getElementById("e2eFillBtn").onclick = () => {
  syncE2EInputs();
  if (e2eStatusEl) e2eStatusEl.textContent = "Form filled from selection.";
};
document.getElementById("e2eFetchBtn").onclick = async () => {
  await fetchE2ELogs();
};

document.getElementById("gitDepType").onchange = updateGitDependencyVisibility;
document.getElementById("gitMigrationEnabled").onchange = updateGitDependencyVisibility;
document.getElementById("zipDepType").onchange = () => updateDependencyVisibility("zip");
document.getElementById("localDepType").onchange = () => updateDependencyVisibility("local");
document.getElementById("zipMigrationEnabled").onchange = () => updateDependencyVisibility("zip");
document.getElementById("localMigrationEnabled").onchange = () => updateDependencyVisibility("local");
updateGitDependencyVisibility();
updateDependencyVisibility("zip");
updateDependencyVisibility("local");

healthCheck();
loadHostInfo();
loadExternalMap();
refreshWorkspaces();
syncE2EInputs();
