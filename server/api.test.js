const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { once } = require("node:events");
const WebSocket = require("ws");

const testDataDir = fs.mkdtempSync(path.join(os.tmpdir(), "kanban-api-test-"));
process.env.KANBAN_DATA_DIR = testDataDir;
process.env.SESSION_SECRET = "api-test-session-secret-at-least-32-characters";
process.env.ALLOW_REGISTRATION = "true";

const { startServer } = require("./index.js");

let server;
let baseUrl;
let sessionCookie;

async function request(pathname, options = {}) {
  const headers = new Headers(options.headers || {});
  if (sessionCookie) headers.set("Cookie", sessionCookie);
  if (options.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  return fetch(`${baseUrl}${pathname}`, { ...options, headers });
}

async function jsonRequest(pathname, options = {}) {
  const response = await request(pathname, options);
  const body = await response.json();
  return { response, body };
}

async function createTask(title, extra = {}) {
  const { response, body } = await jsonRequest("/tasks", {
    method: "POST",
    body: JSON.stringify({ title, description: "", ...extra }),
  });
  assert.equal(response.status, 201);
  return body;
}

test.before(async () => {
  server = startServer(0, "127.0.0.1");
  if (!server.listening) await once(server, "listening");
  const address = server.address();
  baseUrl = `http://127.0.0.1:${address.port}`;
});

test.after(async () => {
  if (server?.listening) {
    await new Promise((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
  fs.rmSync(testDataDir, { recursive: true, force: true });
});

test("startup initializes private data stores", () => {
  for (const fileName of ["tasks.json", "users.json", "wip_limits.json"]) {
    const filePath = path.join(testDataDir, fileName);
    assert.equal(fs.existsSync(filePath), true);
    assert.equal(fs.statSync(filePath).mode & 0o777, 0o600);
  }
});

test("task data and WIP limits require authentication", async () => {
  const tasks = await fetch(`${baseUrl}/tasks`);
  const limits = await fetch(`${baseUrl}/wip-limits`);
  const remedy = await fetch(`${baseUrl}/tasks/missing/remedy`, { method: "POST" });

  assert.equal(tasks.status, 401);
  assert.equal(limits.status, 401);
  assert.equal(remedy.status, 401);
});

test("WebSocket upgrades require an authenticated session", async () => {
  const status = await new Promise((resolve, reject) => {
    const socket = new WebSocket(baseUrl.replace("http:", "ws:"));
    socket.on("unexpected-response", (_request, response) => {
      resolve(response.statusCode);
      response.resume();
    });
    socket.on("open", () => reject(new Error("Unauthenticated socket opened")));
    socket.on("error", () => {});
  });
  assert.equal(status, 401);
});

test("registration establishes a regenerated authenticated session", async () => {
  const response = await fetch(`${baseUrl}/auth/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: "reviewer", password: "correct-horse-42" }),
  });
  assert.equal(response.status, 200);
  sessionCookie = response.headers.get("set-cookie").split(";", 1)[0];

  const { body } = await jsonRequest("/auth/whoami");
  assert.equal(body.authenticated, true);
  assert.equal(body.username, "reviewer");
});

test("authenticated WebSockets receive tasks and reject client messages", async () => {
  const socket = new WebSocket(baseUrl.replace("http:", "ws:"), {
    headers: { Cookie: sessionCookie },
  });
  const [rawMessage] = await once(socket, "message");
  const message = JSON.parse(rawMessage.toString());

  assert.equal(message.type, "tasks");
  assert.equal(Array.isArray(message.data), true);

  socket.send(JSON.stringify({ type: "mutate" }));
  const [code] = await once(socket, "close");
  assert.equal(code, 1008);
});

test("password changes require the current password and revoke other sessions", async () => {
  const secondLogin = await fetch(`${baseUrl}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: "reviewer", password: "correct-horse-42" }),
  });
  assert.equal(secondLogin.status, 200);
  const secondCookie = secondLogin.headers.get("set-cookie").split(";", 1)[0];
  const secondSocket = new WebSocket(baseUrl.replace("http:", "ws:"), {
    headers: { Cookie: secondCookie },
  });
  await once(secondSocket, "message");
  const secondSocketClosed = once(secondSocket, "close");

  const incorrect = await jsonRequest("/auth/change-password", {
    method: "POST",
    body: JSON.stringify({
      currentPassword: "wrong-password",
      newPassword: "updated-horse-84",
    }),
  });
  assert.equal(incorrect.response.status, 401);

  const changedResponse = await request("/auth/change-password", {
    method: "POST",
    body: JSON.stringify({
      currentPassword: "correct-horse-42",
      newPassword: "updated-horse-84",
    }),
  });
  assert.equal(changedResponse.status, 200);
  sessionCookie = changedResponse.headers.get("set-cookie").split(";", 1)[0];

  const [closeCode] = await secondSocketClosed;
  assert.equal(closeCode, 1008);

  const revoked = await fetch(`${baseUrl}/auth/whoami`, {
    headers: { Cookie: secondCookie },
  });
  assert.equal((await revoked.json()).authenticated, false);

  const oldLogin = await fetch(`${baseUrl}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: "reviewer", password: "correct-horse-42" }),
  });
  const newLogin = await fetch(`${baseUrl}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: "reviewer", password: "updated-horse-84" }),
  });
  assert.equal(oldLogin.status, 401);
  assert.equal(newLogin.status, 200);
});

test("state and ownership are server controlled", async () => {
  const task = await createTask("Controlled state");
  const invalid = await jsonRequest(`/tasks/${task.id}/state`, {
    method: "PATCH",
    body: JSON.stringify({ state: "InvisibleColumn" }),
  });
  const forged = await jsonRequest(`/tasks/${task.id}/state`, {
    method: "PATCH",
    body: JSON.stringify({ state: "InProgress", picker: "someone-else" }),
  });
  const claimed = await jsonRequest(`/tasks/${task.id}/state`, {
    method: "PATCH",
    body: JSON.stringify({ state: "InProgress" }),
  });

  assert.equal(invalid.response.status, 400);
  assert.equal(forged.response.status, 400);
  assert.equal(claimed.response.status, 200);
  assert.equal(claimed.body.picker, "reviewer");
});

test("claims are cleared outside In Progress but retained in Done", async () => {
  const waitingTask = await createTask("Auto-declaim task");
  const claimedForWaiting = await jsonRequest(
    `/tasks/${waitingTask.id}/state`,
    {
      method: "PATCH",
      body: JSON.stringify({ state: "InProgress" }),
    },
  );
  const movedToWaiting = await jsonRequest(
    `/tasks/${waitingTask.id}/state`,
    {
      method: "PATCH",
      body: JSON.stringify({ state: "Waiting" }),
    },
  );

  assert.equal(claimedForWaiting.body.picker, "reviewer");
  assert.equal(movedToWaiting.body.picker, null);

  const doneTask = await createTask("Done keeps owner");
  await jsonRequest(`/tasks/${doneTask.id}/state`, {
    method: "PATCH",
    body: JSON.stringify({ state: "InProgress" }),
  });
  const completed = await jsonRequest(`/tasks/${doneTask.id}/state`, {
    method: "PATCH",
    body: JSON.stringify({ state: "Done" }),
  });
  assert.equal(completed.body.picker, "reviewer");
});

test("stale edits are rejected", async () => {
  const task = await createTask("Versioned task");
  const first = await jsonRequest(`/tasks/${task.id}`, {
    method: "PATCH",
    body: JSON.stringify({
      title: "Fresh edit",
      expectedUpdatedAt: task.updated_at,
    }),
  });
  const stale = await jsonRequest(`/tasks/${task.id}`, {
    method: "PATCH",
    body: JSON.stringify({
      title: "Stale edit",
      expectedUpdatedAt: task.updated_at,
    }),
  });

  assert.equal(first.response.status, 200);
  assert.equal(stale.response.status, 409);
});

test("deleting a task preserves its prerequisites", async () => {
  const prerequisite = await createTask("Keep this prerequisite");
  const parent = await createTask("Delete only this task", {
    dependencies: [prerequisite.id],
  });
  const deleted = await jsonRequest(`/tasks/${parent.id}`, { method: "DELETE" });
  const tasks = await jsonRequest("/tasks");

  assert.equal(deleted.response.status, 200);
  assert.deepEqual(deleted.body.deleted, [parent.id]);
  assert.equal(tasks.body.some((task) => task.id === prerequisite.id), true);
  assert.equal(tasks.body.some((task) => task.id === parent.id), false);
});

test("deleting a prerequisite requires confirmation and adjusts dependents", async () => {
  const prerequisite = await createTask("Confirmed prerequisite");
  const dependent = await createTask("Dependent remains", {
    dependencies: [prerequisite.id],
  });
  const refused = await jsonRequest(`/tasks/${prerequisite.id}`, {
    method: "DELETE",
  });
  const confirmed = await jsonRequest(`/tasks/${prerequisite.id}`, {
    method: "DELETE",
    body: JSON.stringify({ confirm: true }),
  });
  const tasks = await jsonRequest("/tasks");
  const adjusted = tasks.body.find((task) => task.id === dependent.id);

  assert.equal(refused.response.status, 409);
  assert.equal(refused.body.dependents[0].id, dependent.id);
  assert.equal(confirmed.response.status, 200);
  assert.deepEqual(adjusted.dependencies, []);
  assert.equal(adjusted.effectiveState, "Ready");
});

test("completion undo internals are not exposed", async () => {
  const task = await createTask("Public undo shape");
  await jsonRequest(`/tasks/${task.id}/state`, {
    method: "PATCH",
    body: JSON.stringify({ state: "InProgress" }),
  });
  await jsonRequest(`/tasks/${task.id}/state`, {
    method: "PATCH",
    body: JSON.stringify({ state: "Done" }),
  });
  const { body: tasks } = await jsonRequest("/tasks");
  const completed = tasks.find((candidate) => candidate.id === task.id);

  assert.equal(completed.completionUndo.available, true);
  assert.equal("previousTask" in completed.completionUndo, false);
});

test("remedies require a blocked task and WIP limits reject negative values", async () => {
  const task = await createTask("Not blocked");
  const remedy = await jsonRequest(`/tasks/${task.id}/remedy`, {
    method: "POST",
    body: JSON.stringify({}),
  });
  const limits = await jsonRequest("/wip-limits", {
    method: "PATCH",
    body: JSON.stringify({ InProgress: -1 }),
  });

  assert.equal(remedy.response.status, 409);
  assert.equal(limits.response.status, 400);
});

test("corrupt task data fails closed without overwriting the file", async () => {
  const tasksPath = path.join(testDataDir, "tasks.json");
  const original = fs.readFileSync(tasksPath, "utf8");
  const corrupt = '{"not": "complete"';
  fs.writeFileSync(tasksPath, corrupt);

  try {
    const response = await request("/tasks");
    const health = await fetch(`${baseUrl}/healthz`);
    assert.equal(response.status, 500);
    assert.equal(health.status, 503);
    assert.equal(fs.readFileSync(tasksPath, "utf8"), corrupt);
  } finally {
    fs.writeFileSync(tasksPath, original);
  }
});
