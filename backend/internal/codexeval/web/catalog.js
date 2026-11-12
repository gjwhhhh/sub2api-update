"use strict";
let groups = [];
let selectedGroup = 0;
let accountList = [];
let accountListReady = false;
let accountListGroup = 0;
let accountListError = "";
let catalogCache = { models: null };
let catalogLoaded = false;
let catalogGroup = 0;
let catalogGeneration = 0;
function resetCatalogCache() {
  catalogGeneration++;
  catalogLoaded = false;
  catalogGroup = 0;
  catalogCache = { models: null };
  accountList = [];
  accountListReady = false;
  accountListGroup = 0;
  accountListError = "";
}
function syncGroupControls() {
  const options = groups.map((g) => [String(g.id), g.name]);
  if (selectedGroup && !groups.some((g) => g.id === selectedGroup)) options.push([String(selectedGroup), `分组 ${selectedGroup}（不可用）`]);
  for (const prefix of ["", "fp-", "pl-", "siwc-"]) {
    if ($(prefix + "group")) fillSelect(prefix + "group", options, String(selectedGroup), String(selectedGroup));
  }
}
// Keep the original model ordering; IDs are sent without CPA suffix conversion.
function compareModels(a, b) {
  const name = (id) => id.toLowerCase().split("/").pop();
  const group = (id) => id.startsWith("gpt-") ? 0 : id.split("(")[0] === "codex-auto-review" ? 1 : id.startsWith("claude-") ? 2 : 3;
  const left = name(a), right = name(b), family = group(left) - group(right);
  const lexical = (x, y) => x < y ? -1 : x > y ? 1 : 0;
  if (family) return family;
  if (group(left) === 0) {
    const generation = /^gpt-(\d+(?:\.\d+)*)(?:-(astra|sol|terra|luna))?(?=[-(]|$)/;
    const l = left.match(generation), r = right.match(generation);
    if (l && r) {
      const lv = l[1].split(".").map(Number), rv = r[1].split(".").map(Number);
      for (let i = 0; i < Math.max(lv.length, rv.length); i++) {
        const version = (rv[i] || 0) - (lv[i] || 0);
        if (version) return version;
      }
      const tiers = ["astra", "sol", "terra", "luna"];
      const rank = (tier) => tier ? tiers.indexOf(tier) : tiers.length;
      const tier = rank(l[2]) - rank(r[2]);
      if (tier) return tier;
    }
  }
  return (group(left) < 3 ? lexical(right, left) : lexical(left, right)) || lexical(a, b);
}

async function refreshCatalog({ signal, force = false } = {}) {
  const reuseModels = catalogLoaded && catalogGroup === selectedGroup && !force;
  const previousModelAccounts = enabledAccountIDs(accountList);
  accountListReady = manualCatalogReady();
  accountListError = "";
  catalogLoaded = false;
  const generation = ++catalogGeneration;
  const initialGroup = selectedGroup;
  const current = () => generation === catalogGeneration && !signal?.aborted;
  let inventoryRead = false;
  try {
    const available = reuseModels ? groups : await api("/api/v1/admin/groups/all", { signal });
    if (!current() || selectedGroup !== initialGroup) return;
    groups = available || [];
    if (!groups.some((g) => g.id === selectedGroup) && !(inspectionSettingsLoaded && selectedGroup > 0)) {
      const remembered = Number(stored(PREF_STORE + ".group"));
      selectedGroup = groups.some((g) => g.id === remembered) ? remembered : groups[0]?.id || 0;
      if (inspectionSettingsLoaded && selectedGroup) markInspectionEdit("group_id");
    }
    syncGroupControls();
    if (!selectedGroup) { accountList = []; catalogCache.models = { ids: [] }; catalogLoaded = true; return; }
    const group = selectedGroup;
    if (accountListGroup !== group) accountListReady = false;
    const inGroup = () => current() && selectedGroup === group;
    const accounts = await fetchNativeAccounts(group, signal, inGroup);
    if (!inGroup()) return;
    accountList = accounts.filter((account) => accountInGroup(account, group));
    accountListReady = true;
    accountListGroup = group;
    inventoryRead = true;
    const { ids, errors } = reuseModels && previousModelAccounts === enabledAccountIDs(accountList)
      ? { ids: catalogCache.models.ids, errors: [] } : await discoverEvaluationModels(accountList, signal);
    if (!inGroup()) return;
    catalogCache.models = { ids };
    fillModels();
    if (errors.length) throw new Error(errors.join("；"));
    catalogLoaded = true;
    catalogGroup = group;
  } catch (err) {
    if (current() && !inventoryRead) {
      accountListReady = false;
      accountListError = err.message;
    }
    throw err;
  }
}

const enabledAccountIDs = (accounts) => accounts.filter((account) => account.schedulable !== false).map((account) => String(account.id)).sort().join(",");
const manualCatalogReady = () => !!selectedGroup && accountListReady && accountListGroup === selectedGroup;
const catalogBlocker = () => manualCatalogReady() ? "" : accountListError ? "账户列表读取失败：" + accountListError : "正在读取账户列表，请稍候";
const accountInGroup = (account, group = selectedGroup) => (account.group_ids || []).some((id) => Number(id) === group);
async function fetchNativeAccounts(group, signal, current = () => !signal?.aborted) {
  const accounts = [];
  const filter = group == null ? "" : `&group=${group}`;
  for (let page = 1; ; page++) {
    const data = await api(`/api/v1/admin/accounts?lite=1&page=${page}&page_size=100${filter}`, { signal });
    if (!current()) return [];
    const items = data.items || [];
    accounts.push(...items);
    if (page >= data.pages || !data.items.length) return accounts;
  }
}

async function discoverEvaluationModels(accounts, signal) {
  const enabled = accounts.filter((account) => account.schedulable !== false);
  const results = await Promise.allSettled(enabled.map((account) => api(`/api/v1/admin/accounts/${account.id}/codex-eval/models`, { signal })));
  if (signal?.aborted) return { ids: [], errors: [] };
  const ids = [], errors = [];
  results.forEach((result, i) => {
    if (result.status === "fulfilled") ids.push(...result.value.map((model) => model.id));
    else {
      if (result.reason instanceof AuthError) throw result.reason;
      errors.push(`${enabled[i].name}：${result.reason.message}`);
    }
  });
  return { ids: [...new Set(ids)].filter((id) => !HIDDEN_MODEL(id)).sort(compareModels), errors };
}
