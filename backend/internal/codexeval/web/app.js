"use strict";
const BASE = "/api/v1/admin/codex-eval";
const DEFAULT_MODEL = "gpt-6.1-sol";
const DEFAULT_EFFORT = "low";
const DEFAULT_EFFORTS = ["none", "low", "medium", "high", "xhigh", "max"];
const HIDDEN_MODEL = (id) => id.toLowerCase().includes("image");
const tabNames = ["candy", "fingerprint", "pelican", "siwc", "settings"];

let sessionBlocked = false;
let credentials = [];
let evaluationState = { active: [], candy: {}, fingerprint: {}, pelican: {}, siwc: {} };
let pollTimer = 0;
let loadError = "";
let loadController = null;
// Whether a refresh started with the button is running; polling never shows progress.
let refreshing = false;
let pending = false;
let storageError = "";
let clearScope = "candy";
// Enable changes in flight, by credential ID; reloads keep these requested states.
const switching = new Map();
const inspectionSwitching = new Map();
const inspectionSwitchErrors = new Map();

function applyTheme() {
 const selected = new URLSearchParams(location.search).get("theme") || localStorage.getItem("theme");
 document.documentElement.dataset.theme = ["light", "dark"].includes(selected) ? selected
   : matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

class AuthError extends Error {}

async function api(path, { method = "GET", body, signal } = {}) {
  const timeout = AbortSignal.timeout(30000);
  const requestKey = localStorage.getItem("auth_token");
  if (!requestKey) throw new AuthError("请登录 Sub2API 管理面板，登录后刷新本页");
  const init = { method, cache: "no-store", signal: signal ? AbortSignal.any([signal, timeout]) : timeout, headers: { Authorization: "Bearer " + requestKey } };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let resp, text;
  try {
    resp = await fetch(path, init);
    text = await resp.text();
  } catch (err) {
    if (signal?.aborted) throw err;
    throw new Error(err.name === "TimeoutError" ? "连接超时，请稍后重试。" : "无法连接 Sub2API，请检查网络后重试。");
  }
  let data = null;
  try { data = JSON.parse(text); } catch (_) {}
  if (resp.status === 401) throw new AuthError("请登录 Sub2API 管理面板，登录后刷新本页");
  if (resp.status === 403) throw new AuthError("无管理员权限");
  if (!resp.ok) {
    const err = data?.error;
    throw Object.assign(new Error(String((typeof err === "string" ? err : err?.message) || data?.message || text || "HTTP " + resp.status)), { status: resp.status });
  }
  if (!data || typeof data !== "object" || Array.isArray(data)) throw new Error("服务器返回了无效数据，请稍后重试。");
  return data.data;
}

function fillModels() {
  const ids = [...new Set(catalogCache.models?.ids || [])].sort(compareModels);
  const probeModels = ids.filter((id) => !/[()]/.test(id));
  const fill = (id, options, saved) => {
    const initial = !$(id).dataset.initialized;
    updateModelPicker(id, options, initial ? saved || DEFAULT_MODEL : undefined);
    $(id).dataset.initialized = "true";
  };
  fill("model", ids, candyPrefs().model);
  fill("fp-model", probeModels, stored(PREF_STORE + ".fingerprint")?.model);
  fill("pl-model", ids, stored(PREF_STORE + ".pelican")?.model);
  fill("siwc-model", probeModels, stored(PREF_STORE + ".siwc")?.model);
  updateModelPicker("inspection-model", ids);
  renderInspectionModelOptions();
}
// Rows are keyed by credential, and an update patches only the markup that changed, so the rest keeps its
// state, such as focus or a loaded preview.
const renderedLists = new Map();
function renderList(prefix, renderRow) {
  const list = $(prefix + "rows"), previous = renderedLists.get(list) || { rows: new Map(), empty: "" };
  const visible = visibleCredentials(prefix);
  if (!visible.length) {
    const message = loadController && !credentials.length ? "正在读取账户和测试记录…" : "没有符合筛选条件的账户，请调整筛选或在 Sub2API 中添加账户。";
    const empty = `<div class="empty">${esc(loadError || message)}</div>`;
    if (previous.empty !== empty) list.innerHTML = empty;
    renderedLists.set(list, { rows: new Map(), empty });
    return;
  }
  const rows = new Map(visible.map((a) => {
    const html = renderRow(a), row = previous.rows.get(a.id);
    if (row?.html === html) return [a.id, row];
    return [a.id, { html, node: row ? patch(row.node, rowNode(html)) : rowNode(html) }];
  }));
  renderedLists.set(list, { rows, empty: "" });
  const kept = new Set([...rows.values()].map((row) => row.node));
  let cursor = list.firstChild;
  const dropStale = () => {
    while (cursor && !kept.has(cursor)) {
      const next = cursor.nextSibling;
      cursor.remove();
      cursor = next;
    }
  };
  for (const { node } of rows.values()) {
    dropStale();
    if (node === cursor) cursor = cursor.nextSibling;
    else list.insertBefore(node, cursor);
  }
  dropStale();
  refreshTip();
}
// patch updates node to match next and returns the node now in place.
function patch(node, next) {
  if (node.isEqualNode(next)) return node;
  if (node.nodeName !== next.nodeName) {
    node.replaceWith(next);
    return next;
  }
  if (node.nodeType !== Node.ELEMENT_NODE) {
    node.nodeValue = next.nodeValue;
    return node;
  }
  for (const { name } of [...node.attributes]) if (!next.hasAttribute(name)) node.removeAttribute(name);
  for (const { name, value } of next.attributes) if (node.getAttribute(name) !== value) node.setAttribute(name, value);
  // Once clicked, a checkbox no longer follows its checked attribute.
  if (node instanceof HTMLInputElement) node.checked = next.checked;
  const children = [...node.childNodes], updates = [...next.childNodes];
  updates.forEach((update, i) => children[i] ? patch(children[i], update) : node.append(update));
  for (const child of children.slice(updates.length)) child.remove();
  return node;
}
function rowNode(html) {
  const template = document.createElement("template");
  template.innerHTML = html;
  return template.content.firstElementChild;
}

function renderPageActions() {
  $("clear-all").disabled = pending || credentials.every((a) => !a.results.length && !a.fingerprints?.length && !a.pelicans?.length && !a.siwc?.length);
  $("refresh").disabled = pending || refreshing;
  $("refresh").setAttribute("aria-busy", refreshing);
}

function render() {
  renderPageActions();
  setNotice("storage-error", storageError);
  for (const prefix of ["", "fp-", "pl-", "siwc-"]) fillPlanFilter(prefix);
  renderCandy();
  renderFingerprints();
  renderPelican();
  renderSIWC();
}

function stopPolling() {
  clearTimeout(pollTimer);
  loadController?.abort();
  loadController = null;
}

function stateCredentials(data) {
 return accountList.filter((account) => accountInGroup(account)).map((account) => {
  const id = String(account.id);
  const task = data.active.find((run) => run.target.account_id === account.id);
  const restrictions = accountRestrictions(account);
  const a = { ...account, id, account_id: account.id, disabled: account.schedulable === false,
   autoInspectionEnabled: inspectionSwitching.has(id) ? inspectionSwitching.get(id) : account.extra?.codex_eval_inspection_enabled !== false,
   unavailable: restrictions.length > 0, restrictions,
   plan_type: account.credentials?.plan_type || account.parent_plan_type || "",
   results: data.candy?.[id] || [], fingerprints: data.fingerprint?.[id] || [],
   pelicans: data.pelican?.[id] || [], siwc: data.siwc?.[id] || [], task,
   task_in_scope: task?.target.group_id === selectedGroup };
  if (task && a.task_in_scope) {
   if (task.type === "candy") a.running = task.progress;
   if (task.type === "fingerprint") a.fingerprint_running = { ...task.progress, phase: task.status === "cancelling" ? "cancelling" : task.progress.phase };
   if (task.type === "pelican") a.pelican_running = true;
   if (task.type === "siwc") a.siwc_running = { ...(task.progress || { done: 0, total: 1 }), phase: task.status === "cancelling" ? "cancelling" : task.progress?.phase };
  }
  return a;
 });
}
function accountRestrictions(account) {
  const restrictions = [];
  if (account.status !== "active") {
    const label = ({ inactive: "账户未激活", error: "账户错误", quota_exceeded: "额度已用尽" })[account.status] || account.status;
    restrictions.push({ label, message: statusText(account.error_message) || label });
  }
  for (const [field, label, reason] of [
    ["temp_unschedulable_until", "暂不可调度", account.temp_unschedulable_reason],
    ["rate_limit_reset_at", "限流中", ""],
    ["overload_until", "过载中", ""],
  ]) {
    if (Date.parse(account[field]) > Date.now()) restrictions.push({ label, message: [statusText(reason), `预计 ${fmtTime(account[field], false)} 恢复`].filter(Boolean).join("\n") });
  }
  return restrictions;
}
const targetsFor = (ids) => (manualCatalogReady() ? ids : []).filter((id) => accountList.some((a) => String(a.id) === String(id) && accountInGroup(a)))
  .map((id) => ({ group_id: selectedGroup, account_id: Number(id) }));

function syncCredentials() {
  credentials = stateCredentials(evaluationState);
  for (const a of credentials) if (switching.has(a.id)) a.disabled = switching.get(a.id);
  fillCredentialTypes();
  const ids = new Set(credentials.map((a) => a.id));
  const usable = new Set(credentials.filter(usableCredential).map((a) => a.id));
  const answers = new Set(credentials.flatMap((a) => a.results.map((r) => answerKey(a.id, r))));
  const pages = new Set(credentials.flatMap((a) => a.pelicans.map((r) => pelicanKey(a.id, r.id))));
  for (const expanded of [candyExpanded, fpExpanded, plExpanded, siwcExpanded]) {
    for (const id of expanded) if (!ids.has(id)) expanded.delete(id);
  }
  for (const selection of [candySelected, fpSelected, plSelected, siwcSelected]) {
    for (const id of selection) if (!usable.has(id)) selection.delete(id);
  }
  for (const id of candyAnswersExpanded) if (!answers.has(id)) candyAnswersExpanded.delete(id);
  // Failed page loads retry with the next state.
  for (const [key, page] of pelicanPages) if (!pages.has(key) || page instanceof Error) pelicanPages.delete(key);
}

async function load({ refresh = false } = {}) {
  stopPolling();
  const controller = new AbortController();
  loadController = controller;
  render();
  try {
    try {
      await refreshCatalog({ signal: controller.signal, force: refresh });
      if (controller.signal.aborted) return;
      fillModels();
      setNotice("catalog-error", "");
    } catch (err) {
      if (controller.signal.aborted) return;
      if (err instanceof AuthError) return showLogin(err.message);
      setNotice("catalog-error", "读取账户和模型列表失败：" + err.message);
      if (!manualCatalogReady()) clearTimeout(notices.get("catalog-error")?.timer);
    }
    if (manualCatalogReady()) {
      syncCredentials();
      render();
    }
    const data = await api(BASE + "/state", { signal: controller.signal });
    if (controller.signal.aborted) return;
    evaluationState = data;
    syncCredentials();
    storageError = data.storage_error || "";
    loadError = "";
    setNotice("load-error", "");
  } catch (err) {
    if (controller.signal.aborted) return;
    if (err instanceof AuthError) return showLogin(err.message);
    loadError = "读取测试记录失败：" + err.message;
    setNotice("load-error", loadError);
  } finally {
    if (loadController === controller) {
      loadController = null;
      render();
      if (!sessionBlocked) pollTimer = setTimeout(load, credentials.some(credentialBusy) ? 2500 : 20000);
    }
  }
}

async function update(path, options, errorPrefix) {
  if (path === "/start" && !manualCatalogReady()) return setNotice("flash", catalogBlocker());
  if (pending) return;
  pending = true;
  stopPolling();
  setNotice("flash", "");
  render();
  try {
    const response = await api(BASE + path, options);
    if (path === "/start") {
      const messages = [`已为 ${response.started.length} 个账户开始测试`];
      if (response.busy.length) messages.push(`${response.busy.length} 个账户正在测试`);
      for (const item of response.unavailable) messages.push(`账户 ${item.target.account_id}：${item.error}`);
      setNotice("flash", messages.join("；"), response.unavailable.length ? "warning" : "info");
    }
    if (path === "/cancel" && response.cancelled) setNotice("flash", "正在取消，等待请求结束", "info");
  } catch (err) {
    if (err instanceof AuthError) return showLogin(err.message);
    setNotice("flash", errorPrefix + "：" + err.message);
  } finally {
    pending = false;
    if (!$("app").hidden) await load();
  }
}

function showLogin(message) {
  stopPolling();
  clearTimeout(inspectionTimer);
  clearTimeout(inspectionAutosaveTimer);
  inspectionStateController?.abort();
  sessionBlocked = true;
  resetCatalogCache();
  credentials = [];
  evaluationState = { active: [], candy: {}, fingerprint: {}, pelican: {}, siwc: {} };
  inspectionState = { results: [] };
  inspectionSettingsLoaded = false;
  inspectionDirtyFields.clear();
  inspectionField("ntfy_password").value = "";
  loadError = storageError = "";
  for (const collection of [candySelected, fpSelected, plSelected, siwcSelected, candyExpanded, fpExpanded, plExpanded, siwcExpanded, candyAnswersExpanded, pelicanPages]) collection.clear();
  for (const id of notices.keys()) setNotice(id, "");
  hideTip();
  document.querySelectorAll("dialog[open]").forEach((dialog) => dialog.close());
  render();
  $("app").hidden = true;
  $("login").hidden = false;
  $("login-error").hidden = !message;
  $("login-error").textContent = message || "";

}

async function start() {
  setNotice("flash", "");
  $("login").hidden = true;
  $("app").hidden = false;
  sessionBlocked = false;
  await loadInspectionSettings();
  fillModels();
  await load({ refresh: true });
}

function setMasked(masked) {
  document.body.classList.toggle("masked", masked);
  for (const id of ["mask", "fp-mask", "pl-mask", "siwc-mask"]) {
    $(id).setAttribute("aria-pressed", masked);
    $(id).innerHTML = icon(masked ? "eye" : "eye-off");
    setLabel($(id), masked ? "取消脱敏" : "脱敏");
  }
}
function switchTab(name, { loadSettings = true } = {}) {
  for (const tab of tabNames) {
    const active = name === tab;
    $("tab-" + tab).setAttribute("aria-selected", active);
    $("tab-" + tab).tabIndex = active ? 0 : -1;
    $(tab + "-panel").hidden = !active;
  }
  hideTip();
  document.querySelector(".page-actions").hidden = name === "settings";
  if (name === "settings" && loadSettings) loadInspectionSettings();
  store(PREF_STORE + ".tab", name);
}
const BUSY_TESTS = [["running", "糖果测试"], ["fingerprint_running", "指纹测试"], ["pelican_running", "鹈鹕测试"], ["siwc_running", "SIWC 检测"]];
const busyTest = (a) => a.task && !(a.task_in_scope ?? a.task.target.group_id === selectedGroup) ? ({ candy: "糖果测试", fingerprint: "指纹测试", pelican: "鹈鹕测试", siwc: "SIWC 检测" }[a.task.type] + "（其他分组）") : BUSY_TESTS.find(([field]) => a[field])?.[1] || "";
const credentialBusy = (a) => !!busyTest(a);
const usableCredential = (a) => !a.disabled && !a.unavailable;
const availableCredential = (a) => usableCredential(a) && !credentialBusy(a);
// Why a test cannot start, shown on its disabled button; empty when it can.
const pageBlocker = (prefix) => !selectedGroup ? "没有可用分组" : catalogBlocker() || (!$(prefix + "model").value.trim() ? "请输入模型名称" : pending ? "正在处理上一个操作，请稍候" : "");
function runBlocker(prefix, a) {
  if (a.disabled) return "账户已停用，启用后才能测试";
  if (a.unavailable) return "账户暂不可用：" + unavailableReason(a);
  if (credentialBusy(a)) return `${busyTest(a)}进行中，结束后才能开始新测试`;
  return pageBlocker(prefix);
}
const runnable = (prefix, a) => !runBlocker(prefix, a);
const selectedCredentials = (prefix, selection) => visibleCredentials(prefix).filter((a) => selection.has(a.id));
const batchCredentials = (prefix, selection) => {
  const selected = selectedCredentials(prefix, selection);
  return (selected.length ? selected : visibleCredentials(prefix)).filter(availableCredential);
};
async function setCredentialEnabled(id, enabled) {
  const a = credentials.find((entry) => entry.id === id);
  if (!a || switching.has(id)) return;
  switching.set(id, !enabled);
  a.disabled = !enabled;
  render();
  try {
    const saved = await api(`/api/v1/admin/accounts/${Number(id)}/schedulable`, { method: "POST", body: { schedulable: enabled } });
    a.schedulable = saved.schedulable; a.disabled = saved.schedulable === false;
  } catch (err) {
    if (err instanceof AuthError) return showLogin(err.message);
    setNotice("flash", (enabled ? "启用账户失败：" : "停用账户失败：") + err.message);
    const current = credentials.find((entry) => entry.id === id);
    if (current) current.disabled = enabled;
  } finally {
    switching.delete(id);
  }
  render();
  // Refresh the native account after its status update.
  if (!$("app").hidden) await load({ refresh: true });
}
async function setAccountInspection(id, enabled) {
  const a = credentials.find((entry) => entry.id === id);
  if (!a || inspectionSwitching.has(id)) return;
  const previous = a.autoInspectionEnabled;
  inspectionSwitching.set(id, enabled);
  inspectionSwitchErrors.delete(id);
  a.autoInspectionEnabled = enabled;
  renderCandy();
  try {
    const saved = await api(`/api/v1/admin/accounts/${Number(id)}/codex-eval-inspection`, { method: "PUT", body: { enabled } });
    for (const account of accountList) if (String(account.id) === id) {
      account.extra = { ...account.extra, codex_eval_inspection_enabled: saved.enabled };
    }
  } catch (err) {
    if (err instanceof AuthError) return showLogin(err.message);
    inspectionSwitchErrors.set(id, "保存失败：" + err.message);
    for (const account of accountList) if (String(account.id) === id) {
      account.extra = { ...account.extra, codex_eval_inspection_enabled: previous };
    }
  } finally {
    inspectionSwitching.delete(id);
    syncCredentials();
    renderCandy();
  }
  if (!$('app').hidden) await load();
}
function renderSelection(prefix, selection, action) {
  const available = visibleCredentials(prefix).filter(availableCredential);
  const selected = selectedCredentials(prefix, selection);
  const targets = batchCredentials(prefix, selection);
  const button = $(prefix + "run-batch");
  button.innerHTML = `${icon("play")}${action}${selected.length ? "所选" : "全部"} (${targets.length})`;
  const blocker = pageBlocker(prefix) || (targets.length ? "" : selected.length ? "所选账户均已停用、不可用或正在测试" : "当前列表没有可测试的账户");
  button.disabled = !!blocker;
  button.dataset.tip = blocker;
  const checkbox = $(prefix + "select-all");
  checkbox.checked = available.length > 0 && available.every((a) => selection.has(a.id));
  checkbox.indeterminate = selected.length > 0 && !checkbox.checked;
  checkbox.disabled = pending || (!available.length && !selected.length);
}

function bindCollectionActions({ type, scope, expanded, renderRows, run, showDetail }) {
  const rows = $(type + "-rows"), dialog = $(type + "-detail");
  $(type + "-detail-close").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => {
    rows.querySelector(`[data-${type}-detail="${CSS.escape(dialog.dataset.record)}"][data-${type}-credential="${CSS.escape(dialog.dataset.credential)}"]`)?.focus();
  });
  rows.addEventListener("click", (e) => {
    if (e.target.closest("label")) return;
    const action = (name) => e.target.closest(`[data-${type}-${name}]`)?.getAttribute(`data-${type}-${name}`);
    const runID = action("run");
    if (runID) return run({ targets: targetsFor([runID]) });
    const cancelID = action("cancel");
    if (cancelID) return update("/cancel", { method: "POST", body: { run_id: credentials.find((a) => a.id === cancelID)?.task?.id } }, "停止测试失败");
    const recordID = action("detail"), credentialID = action("credential");
    if (recordID && credentialID) return openResultDetail(type, scope, credentialID, recordID, showDetail);
    const toggleID = e.target.closest("[data-row]")?.dataset.row;
    if (toggleID) {
      expanded.has(toggleID) ? expanded.delete(toggleID) : expanded.add(toggleID);
      renderRows();
    }
  });
}

function initializeLayout() {
  $("candy-credentials").innerHTML = credentialCard("", "糖果测试", ["账户", "最近一次", "正确率", "最近 20 次"]);
  $("fp-credentials").innerHTML = credentialCard("fp-", "指纹测试", ["账户", "指纹结果", "测试模型", "测试时间"]);
  $("pl-credentials").innerHTML = credentialCard("pl-", "鹈鹕测试", ["账户", "最近一次", "画面"]);
  $("siwc-credentials").innerHTML = credentialCard("siwc-", "SIWC 检测", ["账户", "最近一次", "测试模型", "检测时间"]);
  $("notifications").innerHTML = ["flash", "load-error", "catalog-error", "storage-error"].map((id) => `<div id="${id}" class="global-flash" role="alert" hidden><span class="notice-symbol" aria-hidden="true"></span><span class="notice-message"></span><button class="notice-close" type="button" aria-label="隐藏提示" data-tip>${icon("x")}</button></div>`).join("");
}

// Events and initialization.
initializeLayout();
initializeModelPickers();
initializeTips();
initializeCopyButtons();
initializeCandy();
initializeFingerprint();
initializePelican();
initializeSIWC();
initializeInspection();
document.querySelectorAll("[data-icon]").forEach((el) => { el.outerHTML = icon(el.dataset.icon, el.dataset.class); });

for (const id of ["flash", "load-error", "catalog-error", "storage-error"]) {
  $(id).querySelector(".notice-close").addEventListener("click", () => hideNotice(id));
}

$("refresh").addEventListener("click", async () => {
  if (pending) return;
  resetCatalogCache();
  refreshing = true;
  try {
    await load({ refresh: true });
  } finally {
    refreshing = false;
    renderPageActions();
  }
});

applyTheme();
addEventListener("storage", applyTheme);
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyTheme);

setMasked(stored(MASK_STORE) === true);
$("mask").addEventListener("click", () => {
  const masked = !document.body.classList.contains("masked");
  setMasked(masked);
  store(MASK_STORE, masked);
});
for (const id of ["fp-mask", "pl-mask", "siwc-mask"]) $(id).addEventListener("click", () => $("mask").click());

for (const [id, scope] of [["clear", "candy"], ["fp-clear", "fingerprint"], ["pl-clear", "pelican"], ["siwc-clear", "siwc"], ["clear-all", "all"]]) {
  $(id).addEventListener("click", () => {
    clearScope = scope;
    $("clear-title").textContent = `${$(id).getAttribute("aria-label")}？`;
    $("confirm-clear").returnValue = ""; // Esc keeps the previous returnValue.
    $("confirm-clear").showModal();
  });
}
$("confirm-clear").addEventListener("close", () => {
  if ($("confirm-clear").returnValue !== "confirm") return;
  update("/clear-history", { method: "POST", body: { scope: clearScope } }, "清空测试记录失败");
});

for (const name of tabNames) {
  $("tab-" + name).addEventListener("click", () => switchTab(name));
  $("tab-" + name).addEventListener("keydown", (e) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    const next = e.key === "Home" ? tabNames[0] : e.key === "End" ? tabNames.at(-1) : tabNames[(tabNames.indexOf(name) + (e.key === "ArrowRight" ? 1 : -1) + tabNames.length) % tabNames.length];
    switchTab(next); $("tab-" + next).focus();
  });
}
switchTab(tabNames.includes(stored(PREF_STORE + ".tab")) ? stored(PREF_STORE + ".tab") : "candy", { loadSettings: false });

for (const [prefix, type, selection, submit] of [["", "candy", candySelected, runCandy], ["fp-", "fp", fpSelected, runFingerprint], ["pl-", "pl", plSelected, runPelican], ["siwc-", "siwc", siwcSelected, runSIWC]]) {
  for (const filter of ["credential-type", "credential-plan", "enabled-only"]) $(prefix + filter).addEventListener("change", () => { selection.clear(); render(); });
  $(prefix + "toolbar").addEventListener("submit", (e) => {
    e.preventDefault();
    const ids = batchCredentials(prefix, selection).map((a) => a.id);
    if (ids.length) submit({ targets: targetsFor(ids) });
  });
  $(prefix + "select-all").addEventListener("change", (e) => {
    for (const a of visibleCredentials(prefix)) {
      if (!e.target.checked) selection.delete(a.id);
      else if (availableCredential(a)) selection.add(a.id);
    }
    render();
  });
  $(prefix + "rows").addEventListener("change", (e) => {
    if (e.target.dataset.enable) return setCredentialEnabled(e.target.dataset.enable, e.target.checked);
    if (e.target.dataset.inspectionEnable) return setAccountInspection(e.target.dataset.inspectionEnable, e.target.checked);
    const id = e.target.getAttribute(`data-${type}-select`);
    if (!id) return;
    e.target.checked ? selection.add(id) : selection.delete(id);
    render();
  });
}
document.querySelectorAll("[data-group-select]").forEach((select) => select.addEventListener("change", () => {
 selectedGroup = Number(select.value);
 store(PREF_STORE + ".group", selectedGroup);
 stopPolling(); resetCatalogCache(); credentials = [];
 for (const selection of [candySelected, fpSelected, plSelected, siwcSelected]) selection.clear();
 syncGroupControls(); load({ refresh: true });
}));
localStorage.getItem("auth_token") ? start() : showLogin("请登录 Sub2API 管理面板，登录后刷新本页");
