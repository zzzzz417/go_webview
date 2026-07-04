const API_BASE = `${window.location.origin}/api`;
const ACTIVE_REFRESH_INTERVAL_MS = 1000;
const IDLE_REFRESH_INTERVAL_MS = 3000;
const WEEKDAY_LABELS = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];
const FIXED_TIMESLOTS = buildFixedTimeslots("08:30", "22:00");
const STATE_COLORS = {
  unbooked: "#ffffff",
  pending_release: "#ffffff",
  polling: "#ffffff",
  sold_out: "#ffffff",
  purchased: "#d7f5de",
  high_priority: "#d7f5de",
  none: "#ffffff",
};

const state = {
  auth: {},
  settings: {
    mode: "rule",
    manualDate: "",
    ruleHint: "",
    soldOutStrategy: "disable",
    running: false,
  },
  week: {
    sites: [],
    timeslots: FIXED_TIMESLOTS,
    cells: {},
    selectedSiteId: 0,
    weekStart: "",
  },
  patterns: [],
  logs: [],
  selectedPatternKeys: new Set(),
  refreshTimer: null,
  weekLoading: false,
};

function qs(id) {
  return document.getElementById(id);
}

function buildFixedTimeslots(startHm, endHm) {
  const result = [];
  let current = hmToMinutes(startHm);
  const end = hmToMinutes(endHm);

  while (current < end) {
    const next = current + 30;
    result.push([minutesToHm(current), minutesToHm(next)]);
    current = next;
  }

  return result;
}

function hmToMinutes(hm) {
  const [hours, minutes] = hm.split(":").map(Number);
  return hours * 60 + minutes;
}

function minutesToHm(totalMinutes) {
  const hours = String(Math.floor(totalMinutes / 60)).padStart(2, "0");
  const minutes = String(totalMinutes % 60).padStart(2, "0");
  return `${hours}:${minutes}`;
}

function setVisible(node, visible) {
  node.classList.toggle("hidden", !visible);
}

function setLoginStatus(message = "") {
  qs("login-status").textContent = message;
}

function toast(message) {
  window.alert(message);
}

async function parseJSON(response) {
  const text = await response.text();
  if (!text) {
    return {};
  }
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Error(`接口返回了无效 JSON: ${text}`);
  }
}

async function apiGet(path) {
  const response = await fetch(`${API_BASE}${path}`, { cache: "no-store" });
  return parseJSON(response);
}

async function apiPost(path, payload = {}) {
  const response = await fetch(`${API_BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  return parseJSON(response);
}

function showPage(page) {
  document.querySelectorAll(".page").forEach((node) => node.classList.remove("active"));
  document.querySelectorAll(".nav-btn").forEach((node) => node.classList.remove("active"));

  if (page === "settings") {
    qs("page-settings").classList.add("active");
    qs("nav-settings").classList.add("active");
    return;
  }

  qs("page-main").classList.add("active");
  qs("nav-main").classList.add("active");
}

function showApp(loggedIn) {
  setVisible(qs("login-view"), !loggedIn);
  setVisible(qs("shell-view"), loggedIn);
}

function mergeWeek(nextWeek) {
  const previous = state.week || {};
  state.week = {
    ...previous,
    ...nextWeek,
    sites: Array.isArray(nextWeek?.sites) ? nextWeek.sites : previous.sites || [],
    timeslots: Array.isArray(nextWeek?.timeslots) && nextWeek.timeslots.length ? nextWeek.timeslots : previous.timeslots || FIXED_TIMESLOTS,
    cells: nextWeek?.cells || previous.cells || {},
    selectedSiteId: nextWeek?.selectedSiteId || previous.selectedSiteId || 0,
  };
}

function applySnapshot(snapshot) {
  if (!snapshot) {
    return;
  }

  state.auth = snapshot.auth || {};
  state.settings = {
    ...state.settings,
    ...(snapshot.settings || {}),
  };
  mergeWeek(snapshot.week || {});
  state.patterns = Array.isArray(snapshot.patterns) ? snapshot.patterns : [];
  state.logs = Array.isArray(snapshot.logs) ? snapshot.logs : [];

  renderAuth();
  renderSettings();
  renderSummary();
  renderWeek();
  renderPatterns();
  renderLogs();
  ensureRefreshLoop();
}

function renderAuth() {
  const loggedIn = !!state.auth.loggedIn;
  showApp(loggedIn);

  if (loggedIn) {
    qs("login-password").value = "";
    setLoginStatus("");
  }

  qs("settings-username").value = state.auth.username || "";
  qs("settings-ua").value = state.auth.userAgent || "";
  qs("settings-token").value = state.auth.token || "";
  qs("settings-bearer").value = state.auth.bearer || "";
}

function renderSettings() {
  qs("manual-date").value = state.settings.manualDate || "";
  qs("rule-hint").textContent = state.settings.ruleHint || "";
  qs("sold-out-strategy").value = state.settings.soldOutStrategy || "disable";
  qs("settings-status").textContent = state.settings.running ? "预约程序运行中" : "预约程序已停止";

  document.querySelectorAll("input[name='mode']").forEach((input) => {
    input.checked = input.value === (state.settings.mode || "rule");
  });
}

function renderSummary() {
  qs("summary-site-count").textContent = String((state.week.sites || []).length);
  qs("summary-pattern-count").textContent = String((state.patterns || []).length);
  qs("summary-running").textContent = state.settings.running ? "运行中" : "未运行";
  qs("summary-running").className = `summary-status ${state.settings.running ? "is-running" : ""}`;
}

function renderWeek() {
  renderSiteOptions();
  renderMatrixTable();
}

function renderSiteOptions() {
  const select = qs("site-select");
  const sites = state.week.sites || [];
  const selectedSiteId = Number(state.week.selectedSiteId || 0);

  select.innerHTML = "";

  if (!sites.length) {
    const option = document.createElement("option");
    option.value = "0";
    option.textContent = "暂无场地";
    option.selected = true;
    select.appendChild(option);
    return;
  }

  sites.forEach((site) => {
    const option = document.createElement("option");
    option.value = String(site.siteId);
    option.textContent = site.siteName;
    option.selected = site.siteId === selectedSiteId;
    select.appendChild(option);
  });
}

function renderMatrixTable() {
  const table = qs("matrix-table");
  table.innerHTML = "";

  const thead = document.createElement("thead");
  const headerRow = document.createElement("tr");
  const timeHead = document.createElement("th");
  timeHead.textContent = "时间段";
  headerRow.appendChild(timeHead);

  WEEKDAY_LABELS.forEach((label) => {
    const cell = document.createElement("th");
    cell.textContent = label;
    headerRow.appendChild(cell);
  });

  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  const selectedSiteId = Number(state.week.selectedSiteId || 0);
  const timeslots = Array.isArray(state.week.timeslots) && state.week.timeslots.length
    ? state.week.timeslots
    : FIXED_TIMESLOTS;

  timeslots.forEach(([startHm, endHm]) => {
    const row = document.createElement("tr");
    const labelCell = document.createElement("td");
    labelCell.className = "time-label";
    labelCell.textContent = `${startHm}-${endHm}`;
    row.appendChild(labelCell);

    for (let weekday = 0; weekday < 7; weekday += 1) {
      const key = `${selectedSiteId}|${weekday}|${startHm}`;
      const cellData = state.week.cells[key] || fallbackCell(selectedSiteId, weekday, startHm, endHm);
      row.appendChild(buildMatrixCell(cellData, weekday, startHm, endHm));
    }

    tbody.appendChild(row);
  });

  table.appendChild(tbody);
}

function fallbackCell(siteId, weekday, startHm, endHm) {
  return {
    checked: false,
    disabled: !siteId,
    statusKey: "none",
    rawStatus: "none",
    siteId,
    siteName: "",
    weekday,
    startHm,
    endHm,
  };
}

function buildMatrixCell(cellData, weekday, startHm, endHm) {
  const td = document.createElement("td");
  const wrapper = document.createElement("label");
  const checked = !!cellData.checked;
  wrapper.className = `matrix-cell ${checked ? "is-selected" : ""}`.trim();
  wrapper.style.background = checked
    ? STATE_COLORS.purchased
    : (STATE_COLORS[cellData.statusKey] || "#ffffff");

  const checkbox = document.createElement("input");
  checkbox.type = "checkbox";
  checkbox.checked = checked;
  checkbox.disabled = !state.auth.loggedIn || !!cellData.disabled;
  checkbox.addEventListener("change", async () => {
    const revertValue = !checkbox.checked;
    try {
      const response = await apiPost("/selection/toggle", {
        siteId: Number(state.week.selectedSiteId),
        weekday,
        startHm,
        endHm,
        checked: checkbox.checked,
      });
      await handleSnapshotResponse(response, () => {
        checkbox.checked = revertValue;
      });
    } catch (error) {
      checkbox.checked = revertValue;
      toast(error.message);
    }
  });

  wrapper.appendChild(checkbox);
  td.appendChild(wrapper);
  return td;
}

function renderPatterns() {
  const tbody = qs("selection-table").querySelector("tbody");
  tbody.innerHTML = "";
  state.selectedPatternKeys.clear();

  if (!state.patterns.length) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 5;
    cell.className = "empty-cell";
    cell.textContent = "还没有加入任何预约时段";
    row.appendChild(cell);
    tbody.appendChild(row);
    return;
  }

  state.patterns.forEach((pattern) => {
    const row = document.createElement("tr");
    row.appendChild(buildPatternSelectCell(pattern));
    row.appendChild(buildTextCell(pattern.siteName || ""));
    row.appendChild(buildTextCell(pattern.weekday || ""));
    row.appendChild(buildTextCell(pattern.timeRange || ""));
    row.appendChild(buildStatusCell(pattern.status || "未预约", pattern.statusKey || "unbooked"));
    tbody.appendChild(row);
  });
}

function buildPatternSelectCell(pattern) {
  const td = document.createElement("td");
  const checkbox = document.createElement("input");
  checkbox.type = "checkbox";
  checkbox.checked = false;
  checkbox.disabled = !!state.settings.running;
  checkbox.addEventListener("change", () => {
    if (checkbox.checked) {
      state.selectedPatternKeys.add(pattern.key);
    } else {
      state.selectedPatternKeys.delete(pattern.key);
    }
  });
  td.appendChild(checkbox);
  return td;
}

function buildTextCell(text) {
  const td = document.createElement("td");
  td.textContent = text;
  return td;
}

function buildStatusCell(text, statusKey) {
  const td = document.createElement("td");
  const pill = document.createElement("span");
  pill.className = "state-pill";
  pill.textContent = text;

  if (statusKey === "purchased") {
    pill.style.background = "#d7f5de";
    pill.style.color = "#175d52";
  } else if (statusKey === "pending_release") {
    pill.style.background = "#e4edff";
    pill.style.color = "#305bb4";
  } else if (statusKey === "polling") {
    pill.style.background = "#fff4d6";
    pill.style.color = "#9a6c00";
  } else {
    pill.style.background = "#eef2f4";
    pill.style.color = "#61707a";
  }

  td.appendChild(pill);
  return td;
}

function renderLogs() {
  const logList = qs("log-list");
  logList.innerHTML = "";

  if (!state.logs.length) {
    const empty = document.createElement("div");
    empty.className = "empty-log";
    empty.textContent = "暂无日志";
    logList.appendChild(empty);
    return;
  }

  state.logs.forEach((item) => {
    const line = document.createElement("div");
    line.className = "log-entry";

    const time = document.createElement("span");
    time.className = "log-time";
    time.textContent = `[${item.time || "--:--:--"}]`;

    const message = document.createElement("span");
    message.textContent = item.message || "";

    line.appendChild(time);
    line.appendChild(message);
    logList.appendChild(line);
  });

  logList.scrollTop = logList.scrollHeight;
}

async function handleSnapshotResponse(response, onErrorRevert) {
  if (!response || typeof response !== "object") {
    throw new Error("接口没有返回有效结果");
  }

  if (!response.ok) {
    if (typeof onErrorRevert === "function") {
      onErrorRevert();
    }
    if (response.snapshot) {
      applySnapshot(response.snapshot);
    }
    throw new Error(response.message || "操作失败");
  }

  if (response.snapshot) {
    applySnapshot(response.snapshot);
  }

  return response;
}

async function refreshState() {
  try {
    const response = await apiGet("/state");
    if (response.snapshot) {
      applySnapshot(response.snapshot);
      await ensureWeekLoaded();
    }
  } catch (error) {
    console.error(error);
  }
}

async function ensureWeekLoaded() {
  if (!state.auth.loggedIn || state.weekLoading) {
    return;
  }
  if (Array.isArray(state.week.sites) && state.week.sites.length > 0 && Number(state.week.selectedSiteId || 0) > 0) {
    return;
  }

  state.weekLoading = true;
  try {
    const response = await apiPost("/week", {
      weekStart: state.week.weekStart || "",
    });
    await handleSnapshotResponse(response);
  } catch (error) {
    console.error(error);
  } finally {
    state.weekLoading = false;
  }
}

function ensureRefreshLoop() {
  const interval = state.settings.running ? ACTIVE_REFRESH_INTERVAL_MS : IDLE_REFRESH_INTERVAL_MS;
  if (state.refreshTimer && state.refreshTimer.interval === interval) {
    return;
  }
  if (state.refreshTimer) {
    clearInterval(state.refreshTimer.id);
  }
  const id = window.setInterval(refreshState, interval);
  state.refreshTimer = { id, interval };
}

async function login() {
  const username = qs("login-username").value.trim();
  const password = qs("login-password").value;

  if (!username || !password) {
    setLoginStatus("请输入账号和密码");
    return;
  }

  const button = qs("login-submit");
  button.disabled = true;
  setLoginStatus("登录中...");

  try {
    const response = await apiPost("/auth", { username, password });
    await handleSnapshotResponse(response);
    await ensureWeekLoaded();
    setLoginStatus("");
  } catch (error) {
    setLoginStatus(error.message);
  } finally {
    button.disabled = false;
  }
}

async function removeSelectedPatterns() {
  const keys = Array.from(state.selectedPatternKeys);
  if (!keys.length) {
    toast("请先勾选要删除的规则");
    return;
  }

  try {
    const response = await apiPost("/selection/remove", { keys });
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function clearPatterns() {
  try {
    const response = await apiPost("/selection/clear");
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function setMode(mode) {
  try {
    const response = await apiPost("/settings/mode", { mode });
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function setManualDate(manualDate) {
  try {
    const response = await apiPost("/settings/manual-date", { manualDate });
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function setSoldOutStrategy(soldOutStrategy) {
  try {
    const response = await apiPost("/settings/sold-out-strategy", { soldOutStrategy });
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function changeSite(siteId) {
  try {
    const response = await apiPost("/site", { siteId: Number(siteId) });
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function startBooking() {
  try {
    const response = await apiPost("/booking/start");
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function stopBooking() {
  try {
    const response = await apiPost("/booking/stop");
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function refreshToken() {
  try {
    const response = await apiPost("/token/refresh");
    await handleSnapshotResponse(response);
  } catch (error) {
    toast(error.message);
  }
}

async function relogin() {
  try {
    const response = await apiPost("/relogin");
    await handleSnapshotResponse(response);
    qs("login-password").value = "";
    showPage("main");
  } catch (error) {
    toast(error.message);
  }
}

function bindEvents() {
  qs("login-submit").addEventListener("click", login);
  qs("login-password").addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      login();
    }
  });

  qs("nav-main").addEventListener("click", () => showPage("main"));
  qs("nav-settings").addEventListener("click", () => showPage("settings"));

  qs("site-select").addEventListener("change", (event) => {
    changeSite(event.target.value);
  });

  qs("selection-remove").addEventListener("click", removeSelectedPatterns);
  qs("selection-clear").addEventListener("click", clearPatterns);
  qs("booking-start").addEventListener("click", startBooking);
  qs("booking-stop").addEventListener("click", stopBooking);
  qs("settings-refresh-token").addEventListener("click", refreshToken);
  qs("settings-relogin").addEventListener("click", relogin);

  document.querySelectorAll("input[name='mode']").forEach((input) => {
    input.addEventListener("change", (event) => {
      if (event.target.checked) {
        setMode(event.target.value);
      }
    });
  });

  qs("manual-date").addEventListener("change", (event) => {
    if (event.target.value) {
      setManualDate(event.target.value);
    }
  });

  qs("sold-out-strategy").addEventListener("change", (event) => {
    setSoldOutStrategy(event.target.value);
  });
}

function initStaticView() {
  renderWeek();
  renderPatterns();
  renderLogs();
}

document.addEventListener("DOMContentLoaded", () => {
  bindEvents();
  initStaticView();
  refreshState();
});
