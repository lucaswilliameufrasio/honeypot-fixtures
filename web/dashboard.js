"use strict";

const tokenInput = document.querySelector("#token");
const connectButton = document.querySelector("#connect");
const refreshButton = document.querySelector("#refresh");
const saveButton = document.querySelector("#save");
const configForm = document.querySelector("#config-form");
const configInput = document.querySelector("#upstream-url");
const connectionMessage = document.querySelector("#connection-message");
const configMessage = document.querySelector("#config-message");
const eventsBody = document.querySelector("#events");

let bearerToken = "";
let configRevision = "";

async function request(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${bearerToken}`);
  if (options.body) {
    headers.set("Content-Type", "application/json");
  }
  const response = await fetch(path, { ...options, headers, cache: "no-store" });
  const body = await response.json();
  if (!response.ok) {
    throw new Error(body.message || `HTTP ${response.status}`);
  }
  return body;
}

function renderEvents(events) {
  eventsBody.replaceChildren();
  if (events.length === 0) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 5;
    cell.className = "muted";
    cell.textContent = "Nenhuma requisição registrada nesta execução.";
    row.append(cell);
    eventsBody.append(row);
    return;
  }

  for (const event of [...events].reverse()) {
    const row = document.createElement("tr");
    for (const value of [
      new Date(event.time).toLocaleTimeString(),
      event.kind === "honeypot" ? "Honeypot" : "Proxy",
      event.method,
      event.path,
      String(event.status),
    ]) {
      const cell = document.createElement("td");
      cell.textContent = value;
      row.append(cell);
    }
    eventsBody.append(row);
  }
}

async function refreshDashboard() {
  const [status, config] = await Promise.all([
    request("/v1/status"),
    request("/v1/config"),
  ]);
  document.querySelector("#honeypot-hits").textContent = String(status.honeypot_hits);
  document.querySelector("#proxy-hits").textContent = String(status.proxied_hits);
  document.querySelector("#upstream-state").textContent = status.upstream_configured ? "Configurado" : "Ausente";
  document.querySelector("#uptime").textContent = `${status.uptime_seconds}s`;
  configInput.value = config.upstream_url || "";
  configRevision = config.revision;
  renderEvents(status.recent_events || []);
  connectionMessage.textContent = "Conectado. Atualização automática a cada 5 segundos.";
  saveButton.disabled = false;
  refreshButton.disabled = false;
}

connectButton.addEventListener("click", async () => {
  bearerToken = tokenInput.value;
  try {
    await refreshDashboard();
    tokenInput.value = "";
  } catch (error) {
    bearerToken = "";
    connectionMessage.textContent = error.message;
  }
});

refreshButton.addEventListener("click", async () => {
  try {
    await refreshDashboard();
  } catch (error) {
    connectionMessage.textContent = error.message;
  }
});

configForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  configMessage.textContent = "";
  try {
    const config = await request("/v1/config", {
      method: "PUT",
      headers: { "If-Match": `"${configRevision}"` },
      body: JSON.stringify({ upstream_url: configInput.value }),
    });
    configRevision = config.revision;
    configMessage.textContent = "Configuração aplicada.";
    await refreshDashboard();
  } catch (error) {
    configMessage.textContent = error.message;
  }
});

window.setInterval(async () => {
  if (bearerToken !== "") {
    try {
      await refreshDashboard();
    } catch (error) {
      connectionMessage.textContent = error.message;
    }
  }
}, 5000);
