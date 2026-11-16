"use strict";

const $ = (id) => document.getElementById(id);
let mode = "login";
let currentIdentity = null;
let busy = false;
let refreshing = false;
let refreshTask = null;
let toastTimer;
const channel = typeof BroadcastChannel === "function" ? new BroadcastChannel("session-account") : null;
const dateFormat = new Intl.DateTimeFormat("ru-RU", { day: "numeric", month: "short", year: "numeric" });
const timeFormat = new Intl.DateTimeFormat("ru-RU", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

function notify(message, error = false) {
  clearTimeout(toastTimer);
  $("toast").textContent = message;
  $("toast").classList.toggle("error", error);
  $("toast").hidden = false;
  toastTimer = setTimeout(() => { $("toast").hidden = true; }, error ? 8000 : 4500);
}

function showAuth() {
  currentIdentity = null;
  $("boot").hidden = true;
  $("dashboard-view").hidden = true;
  $("auth-view").hidden = false;
  $("session-list").replaceChildren();
  $("account-email").textContent = "";
  if ($("confirm-dialog").open) $("confirm-dialog").close();
  $("password").value = "";
  setMode("login", true);
}

class APIError extends Error {
  constructor(message, status) { super(message); this.status = status; }
}

async function request(path, options = {}, protectedRequest = true) {
  let response;
  try {
    response = await fetch(path, { credentials: "same-origin", cache: "no-store", signal: AbortSignal.timeout(12000), ...options });
  } catch (error) {
    throw new APIError(error.name === "TimeoutError" ? "Сервер не ответил вовремя. Попробуйте снова." : "Нет соединения с сервером. Проверьте подключение.", 0);
  }
  const data = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    if (response.status === 401 && protectedRequest) showAuth();
    const retry = response.headers.get("Retry-After");
    const message = data?.error?.message || "Не удалось выполнить запрос. Попробуйте снова.";
    throw new APIError(message + (response.status === 429 && retry ? ` Повторите примерно через ${Math.ceil(Number(retry) / 60)} мин.` : ""), response.status);
  }
  return data;
}

function setMode(nextMode, force = false) {
  if (busy && !force) return;
  mode = nextMode;
  const register = mode === "register";
  for (const name of ["login", "register"]) {
    const selected = mode === name;
    $(`${name}-tab`).setAttribute("aria-selected", String(selected));
    $(`${name}-tab`).tabIndex = selected ? 0 : -1;
  }
  $("auth-panel").setAttribute("aria-labelledby", `${mode}-tab`);
  $("auth-title").textContent = register ? "Начнём знакомство" : "С возвращением";
  $("auth-subtitle").textContent = register ? "Создайте аккаунт за пару шагов." : "Введите данные своего аккаунта.";
  $("auth-submit-label").textContent = register ? "Создать аккаунт" : "Войти в аккаунт";
  $("password").autocomplete = register ? "new-password" : "current-password";
  $("password").minLength = register ? 12 : 1;
  $("password-hint").textContent = register ? "Не менее 12 символов. Можно использовать пробелы." : "Пароль вашего аккаунта.";
  $("form-error").hidden = true;
}

function deviceName(agent) {
  const browser = /Edg\//.test(agent) ? "Edge" : /OPR\//.test(agent) ? "Opera" : /Firefox\//.test(agent) ? "Firefox" : /Chrome\//.test(agent) ? "Chrome" : /Safari\//.test(agent) ? "Safari" : "Браузер / API-клиент";
  const os = /Android/.test(agent) ? "Android" : /iPhone|iPad/.test(agent) ? "iOS" : /Windows/.test(agent) ? "Windows" : /Macintosh|Mac OS/.test(agent) ? "macOS" : /Linux/.test(agent) ? "Linux" : "Другое устройство";
  return `${browser} · ${os}`;
}

function remaining(expires) {
  const minutes = Math.max(0, Math.ceil((new Date(expires) - Date.now()) / 60000));
  if (minutes === 0) return "Истекла";
  if (minutes < 60) return `${minutes} мин`;
  const hours = Math.floor(minutes / 60);
  return hours >= 24 ? `${Math.floor(hours / 24)} д ${hours % 24} ч` : `${hours} ч ${minutes % 60} мин`;
}

function renderSessions(sessions) {
  const fragment = document.createDocumentFragment();
  for (const session of sessions) {
    const row = $("session-template").content.firstElementChild.cloneNode(true);
    row.classList.toggle("current", session.current);
    row.querySelector("h3").textContent = deviceName(session.user_agent);
    row.querySelector(".current-badge").hidden = !session.current;
    row.querySelector(".device-meta").textContent = `IP: ${session.ip || "не определён"}`;
    row.querySelector(".created-time").textContent = timeFormat.format(new Date(session.created_at));
    row.querySelector(".expires-time").textContent = `До ${timeFormat.format(new Date(session.expires_at))}`;
    const button = row.querySelector(".revoke-button");
    button.setAttribute("aria-label", `Завершить сессию ${deviceName(session.user_agent)}${session.current ? ", текущее устройство" : ""}`);
    button.addEventListener("click", () => action(button, async () => {
      await request(`/api/sessions/${encodeURIComponent(session.id)}`, { method: "DELETE" });
      if (session.current) showAuth(); else await refresh();
      channel?.postMessage("sessions-changed");
      notify(session.current ? "Вы вышли из аккаунта." : "Сессия завершена. Доступ устройства отозван.");
    }));
    fragment.append(row);
  }
  $("session-list").replaceChildren(fragment);
  $("session-count").textContent = sessions.length;
  $("session-pill").textContent = sessions.length;
  $("sessions-empty").hidden = sessions.length > 0;
  $("updated-at").textContent = `Обновлено в ${new Date().toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })}`;
}

function refresh() {
  if (refreshTask) return refreshTask;
  refreshTask = loadDashboard().finally(() => { refreshTask = null; });
  return refreshTask;
}

async function loadDashboard() {
  refreshing = true;
  try {
    const id = await request("/api/me");
    const data = await request("/api/sessions");
    currentIdentity = id;
    $("account-email").textContent = id.user.email;
    $("avatar").textContent = id.user.email[0].toUpperCase();
    $("account-created").textContent = dateFormat.format(new Date(id.user.created_at));
    $("session-remaining").textContent = remaining(id.session.expires_at);
    renderSessions(data.sessions);
    $("boot").hidden = true;
    $("auth-view").hidden = true;
    $("dashboard-view").hidden = false;
  } finally { refreshing = false; }
}

async function checkReady() {
  let ready = false;
  try { await request("/ready", {}, false); ready = true; } catch { /* status is shown in the header */ }
  $("service-status").classList.toggle("offline", !ready);
  $("service-label").textContent = ready ? "Сервис доступен" : "Сервис недоступен";
}

async function action(button, fn) {
  if (busy) return;
  busy = true;
  button.disabled = true;
  button.setAttribute("aria-busy", "true");
  try {
    // Finish an in-flight read before a mutation can change authentication.
    if (refreshTask) await refreshTask.catch(() => {});
    await fn();
  } catch (error) { notify(error.message, true); }
  finally { busy = false; button.disabled = false; button.removeAttribute("aria-busy"); }
}

$("login-tab").addEventListener("click", () => setMode("login"));
$("register-tab").addEventListener("click", () => setMode("register"));
document.querySelector(".auth-tabs").addEventListener("keydown", (event) => {
  if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
    event.preventDefault();
    setMode(event.key === "Home" ? "login" : event.key === "End" ? "register" : mode === "login" ? "register" : "login");
    $(`${mode}-tab`).focus();
  }
});
$("toggle-password").addEventListener("click", () => {
  const show = $("password").type === "password";
  $("password").type = show ? "text" : "password";
  $("toggle-password").textContent = show ? "Скрыть" : "Показать";
  $("toggle-password").setAttribute("aria-pressed", String(show));
});
$("auth-form").addEventListener("submit", (event) => {
  event.preventDefault();
  action($("auth-submit"), async () => {
    $("form-error").hidden = true;
    try {
      await request(`/api/auth/${mode}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email: $("email").value.trim(), password: $("password").value }) }, false);
      $("password").value = "";
      $("password").type = "password";
      $("toggle-password").textContent = "Показать";
      $("toggle-password").setAttribute("aria-pressed", "false");
      await refresh();
      channel?.postMessage("sessions-changed");
      $("main").focus();
    } catch (error) {
      $("form-error").textContent = error.message;
      $("form-error").hidden = false;
    }
  });
});
$("refresh").addEventListener("click", () => action($("refresh"), async () => { await refresh(); await checkReady(); notify("Список сессий обновлён."); }));
$("logout").addEventListener("click", () => action($("logout"), async () => {
  await request("/api/auth/logout", { method: "POST" });
  showAuth();
  channel?.postMessage("sessions-changed");
  notify("Вы вышли из аккаунта.");
}));
$("logout-all").addEventListener("click", () => { if (!busy) $("confirm-dialog").showModal(); });
$("cancel-logout-all").addEventListener("click", () => $("confirm-dialog").close());
$("confirm-logout-all").addEventListener("click", () => action($("confirm-logout-all"), async () => {
  await request("/api/auth/logout-all", { method: "POST" });
  $("confirm-dialog").close();
  showAuth();
  channel?.postMessage("sessions-changed");
  notify("Все сессии завершены.");
}));

async function backgroundRefresh() {
  if (busy || refreshing || document.hidden) return;
  await checkReady();
  if (currentIdentity && !busy) {
    try { await refresh(); } catch (error) { if (error.status === 401) notify("Сессия завершена. Войдите снова."); }
  }
}
channel?.addEventListener("message", () => { if (!busy) refresh().catch((error) => { if (error.status !== 401) notify(error.message, true); }); });
document.addEventListener("visibilitychange", backgroundRefresh);
setInterval(backgroundRefresh, 60000);

async function boot() {
  setMode("login");
  await checkReady();
  try { await refresh(); } catch (error) { showAuth(); if (error.status !== 401) notify(error.message, true); }
}
boot();
