"use strict";
const PLATFORM_LABELS = {
  openai: "OpenAI",
  anthropic: "Anthropic",
  gemini: "Gemini",
  antigravity: "Antigravity",
  grok: "Grok",
  kimi: "Kimi",
  zhipu: "Zhipu GLM",
  deepseek: "DeepSeek",
  minimax: "MiniMax",
  opencode_go: "OpenCode",
  typesafe: "TypeSafe",
  command_code: "Command Code",
  cline: "Cline",
};
const AUTH_TYPE_LABELS = {
  oauth: "OAuth",
  "setup-token": "Setup Token",
  apikey: "API Key",
  upstream: "Upstream",
};

const accountPlatform = (a) => String(a?.platform || "openai").trim().toLowerCase() || "openai";
const providerType = (a) => accountPlatform(a) === "openai" ? a.type : `${accountPlatform(a)}:${a.type}`;
const credentialType = (a) => `${accountPlatform(a)}:${a.type}`;
function credentialTypeLabel(value) {
  const key = typeof value === "string" ? value : credentialType(value);
  if (key === "all") return "全部账户";
  if (!key.includes(":")) return AUTH_TYPE_LABELS[key] || key;
  const [platform, type] = key.split(/:(.*)/);
  return `${PLATFORM_LABELS[platform] || platform} · ${AUTH_TYPE_LABELS[type] || type}`;
}
function matchesCredentialType(a, selected, prefix = "") {
  if (prefix === "siwc-") return accountPlatform(a) === "openai" && a.type === "oauth";
  if (!selected || selected === "all") return true;
  if (selected.includes(":")) return credentialType(a) === selected;
  return a.type === selected && accountPlatform(a) === "openai";
}
const typedCredentials = (prefix) => credentials.filter((a) => matchesCredentialType(a, $(prefix + "credential-type")?.value, prefix));
const PRO_ALL = "Pro ALL";
function visibleCredentials(prefix) {
  const filter = $(prefix + "credential-plan").value;
  const enabledOnly = $(prefix + "enabled-only").checked;
  const matches = (plan) => filter === "all" || (filter === PRO_ALL ? plan?.pro : plan?.label === filter);
  return typedCredentials(prefix).filter((a) => (!enabledOnly || !a.disabled) && matches(credentialPlan(a))).sort((a, b) => planRank(a) - planRank(b));
}
function fillPlanFilter(prefix) {
  const plans = [...new Map(typedCredentials(prefix).map(credentialPlan).filter(Boolean).map((plan) => [plan.label, plan])).values()]
    .sort((a, b) => a.rank - b.rank || a.label.localeCompare(b.label));
  const groups = plans.filter((plan) => plan.pro).length > 1 ? [PRO_ALL] : [];
  const selected = $(prefix + "credential-plan").value;
  const options = [["all", "所有订阅类型"], ...groups, ...plans.map((plan) => plan.label)];
  if (!prefix && selected && selected !== "all" && !options.some((option) => (Array.isArray(option) ? option[0] : option) === selected)) options.push(selected);
  $(prefix + "plan-filter").hidden = !plans.length && !(selected && selected !== "all" && !prefix);
  fillSelect(prefix + "credential-plan", options, selected, "all");
}
function fillCredentialTypes() {
  const types = new Map(credentials.map((a) => [credentialType(a), credentialTypeLabel(a)]));
  const options = [["all", "全部账户"], ...[...types].sort(([a], [b]) => a.localeCompare(b))];
  for (const prefix of ["", "fp-", "pl-", "siwc-"]) {
    const select = $(prefix + "credential-type");
    if (!select) continue;
    if (prefix === "siwc-") {
      select.parentElement.hidden = true;
      fillSelect(prefix + "credential-type", [["openai:oauth", credentialTypeLabel("openai:oauth")]], "openai:oauth", "openai:oauth");
      continue;
    }
    const selected = select.value;
    const values = [...options];
    if (selected && selected !== "all" && !values.some(([value]) => value === selected)) {
      values.push([selected, credentialTypeLabel(selected)]);
    }
    fillSelect(prefix + "credential-type", values, selected, "all");
  }
}
