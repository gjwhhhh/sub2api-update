"use strict";
const PREF_STORE = "sub2api-codex-eval.prefs";
const MASK_STORE = "sub2api-codex-eval.masked";

function stored(name) {
  try { return JSON.parse(localStorage.getItem(name)); } catch (_) { return null; }
}
function store(name, value) {
  try { localStorage.setItem(name, JSON.stringify(value)); } catch (_) {}
}
function preference(name, fallback) {
  const value = stored(PREF_STORE + "." + name);
  return value !== null && typeof value === typeof fallback ? value : fallback;
}
function savePreference(name, value) {
  store(PREF_STORE + "." + name, value);
}
