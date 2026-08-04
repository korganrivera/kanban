"use strict";
const express = require("express");
const app = express();
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");
const { WebSocketServer } = require("ws");
const session = require("express-session");
const bcrypt = require("bcrypt");
const {
  TASK_STATES,
  anyDependencyUnresolved,
  calcReadyAt,
  computeEffectiveState,
  hasActiveClaim,
  parseDateSafe,
} = require("./static/task-state.js");

const DATA_DIR = process.env.KANBAN_DATA_DIR
  ? path.resolve(process.env.KANBAN_DATA_DIR)
  : path.join(__dirname, "data");
const BACKUP_DIR = process.env.KANBAN_BACKUP_DIR
  ? path.resolve(process.env.KANBAN_BACKUP_DIR)
  : path.join(DATA_DIR, "backups");
fs.mkdirSync(DATA_DIR, { recursive: true, mode: 0o700 });
fs.chmodSync(DATA_DIR, 0o700);

const TASKS_FILE = path.join(DATA_DIR, "tasks.json");
const USERS_FILE = path.join(DATA_DIR, "users.json");
const WIP_LIMITS_FILE = path.join(DATA_DIR, "wip_limits.json");
const SESSIONS_FILE = path.join(DATA_DIR, "sessions.json");
const SESSION_SECRET_FILE = path.join(DATA_DIR, "session-secret");
const TRANSACTION_FILE = path.join(DATA_DIR, "pending-transaction.json");

for (const privateFile of [
  TASKS_FILE,
  USERS_FILE,
  WIP_LIMITS_FILE,
  SESSIONS_FILE,
  SESSION_SECRET_FILE,
]) {
  try {
    if (fs.existsSync(privateFile)) fs.chmodSync(privateFile, 0o600);
  } catch (error) {
    console.error(`Could not secure ${privateFile}:`, error);
    throw error;
  }
}

function fsyncDirectory(directory) {
  let directoryFd;
  try {
    directoryFd = fs.openSync(directory, "r");
    fs.fsyncSync(directoryFd);
  } finally {
    if (directoryFd !== undefined) fs.closeSync(directoryFd);
  }
}

function writeFileAtomic(filePath, content, mode = 0o600) {
  const directory = path.dirname(filePath);
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  const tempPath = `${filePath}.tmp-${process.pid}-${Date.now()}-${crypto.randomBytes(4).toString("hex")}`;
  let fileFd;
  try {
    fileFd = fs.openSync(tempPath, "wx", mode);
    fs.writeFileSync(fileFd, content, "utf8");
    fs.fsyncSync(fileFd);
    fs.closeSync(fileFd);
    fileFd = undefined;
    fs.renameSync(tempPath, filePath);
    fs.chmodSync(filePath, mode);
    fsyncDirectory(directory);
  } catch (error) {
    if (fileFd !== undefined) fs.closeSync(fileFd);
    try {
      fs.unlinkSync(tempPath);
    } catch (cleanupError) {
      if (cleanupError.code !== "ENOENT") {
        console.error(`Failed to clean up ${tempPath}:`, cleanupError);
      }
    }
    throw error;
  }
}

function quarantineCorruptFile(filePath) {
  const quarantinePath = `${filePath}.corrupt-${new Date()
    .toISOString()
    .replace(/[:.]/g, "-")}`;
  fs.renameSync(filePath, quarantinePath);
  return quarantinePath;
}

function getSessionSecret() {
  const configured = String(process.env.SESSION_SECRET || "").trim();
  if (configured) return configured;
  try {
    const existing = fs.readFileSync(SESSION_SECRET_FILE, "utf8").trim();
    if (existing.length >= 32) {
      fs.chmodSync(SESSION_SECRET_FILE, 0o600);
      return existing;
    }
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  const generated = crypto.randomBytes(48).toString("hex");
  writeFileAtomic(SESSION_SECRET_FILE, `${generated}\n`, 0o600);
  return generated;
}

class JsonSessionStore extends session.Store {
  constructor(filePath) {
    super();
    this.filePath = filePath;
    this.sessions = this.load();
    this.pruneExpired();
  }

  load() {
    try {
      const raw = fs.readFileSync(this.filePath, "utf8");
      if (!raw || raw.trim() === "") return {};
      const parsed = JSON.parse(raw);
      if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
        throw new Error("Session store must contain an object");
      }
      return parsed;
    } catch (err) {
      if (err && err.code === "ENOENT") return {};
      console.error(`Error loading session store ${this.filePath}:`, err);
      try {
        const quarantined = quarantineCorruptFile(this.filePath);
        console.error(`Corrupt sessions quarantined at ${quarantined}`);
      } catch (quarantineError) {
        console.error("Failed to quarantine corrupt sessions:", quarantineError);
      }
      return {};
    }
  }

  save() {
    try {
      writeFileAtomic(
        this.filePath,
        `${JSON.stringify(this.sessions, null, 2)}\n`,
        0o600,
      );
    } catch (err) {
      console.error(`Error saving session store ${this.filePath}:`, err);
      throw err;
    }
  }

  isExpired(sess) {
    const expiresAt = sess && sess.cookie && sess.cookie.expires;
    if (!expiresAt) return false;
    const ts = new Date(expiresAt).getTime();
    return Number.isFinite(ts) && ts <= Date.now();
  }

  pruneExpired() {
    let changed = false;
    for (const [sid, sess] of Object.entries(this.sessions)) {
      if (this.isExpired(sess)) {
        delete this.sessions[sid];
        changed = true;
      }
    }
    if (changed) this.save();
  }

  get(sid, cb) {
    const sess = this.sessions[sid];
    if (!sess) return cb(null, null);
    if (this.isExpired(sess)) {
      delete this.sessions[sid];
      try {
        this.save();
        return cb(null, null);
      } catch (error) {
        return cb(error);
      }
    }
    cb(null, sess);
  }

  set(sid, sess, cb) {
    try {
      this.sessions[sid] = sess;
      this.save();
      cb && cb(null);
    } catch (error) {
      cb && cb(error);
    }
  }

  destroy(sid, cb) {
    try {
      if (sid in this.sessions) {
        delete this.sessions[sid];
        this.save();
      }
      cb && cb(null);
    } catch (error) {
      cb && cb(error);
    }
  }

  touch(sid, sess, cb) {
    try {
      this.sessions[sid] = sess;
      this.save();
      cb && cb(null);
    } catch (error) {
      cb && cb(error);
    }
  }

  destroyByUserId(userId, cb) {
    try {
      let changed = false;
      for (const [sid, sess] of Object.entries(this.sessions)) {
        if (sess?.userId === userId) {
          delete this.sessions[sid];
          changed = true;
        }
      }
      if (changed) this.save();
      cb && cb(null);
    } catch (error) {
      cb && cb(error);
    }
  }
}

const sessionStore = new JsonSessionStore(SESSIONS_FILE);
const sessionPruneTimer = setInterval(
  () => {
    try {
      sessionStore.pruneExpired();
    } catch (error) {
      console.error("Session pruning failed:", error);
    }
  },
  60 * 60 * 1000,
);
sessionPruneTimer.unref();

app.disable("x-powered-by");
app.use(express.json({ limit: "256kb" }));

// Session configuration
const sessionMiddleware = session({
  secret: getSessionSecret(),
  store: sessionStore,
  resave: false,
  saveUninitialized: false,
  rolling: true,
  cookie: {
    maxAge: 1000 * 60 * 60 * 24 * 7,
    httpOnly: true,
    sameSite: "lax",
    secure: process.env.COOKIE_SECURE === "true",
  },
});
app.use(sessionMiddleware);
app.use((req, res, next) => {
  res.set("X-Content-Type-Options", "nosniff");
  res.set("X-Frame-Options", "DENY");
  res.set("Referrer-Policy", "same-origin");
  if (!["GET", "HEAD", "OPTIONS"].includes(req.method)) {
    const origin = req.get("Origin");
    if (origin) {
      try {
        if (new URL(origin).host !== req.get("Host")) {
          return res.status(403).json({ error: "Cross-origin request denied" });
        }
      } catch (error) {
        return res.status(400).json({ error: "Invalid request origin" });
      }
    }
  }
  next();
});

// Serve static files AFTER session setup
app.use(express.static(path.join(__dirname, "static")));

/* -------------------- configuration -------------------- */

const DEFAULT_WIP_LIMITS = {
  Ready: null,
  InProgress: 5,
  Blocked: 10,
  Suspended: null,
  Waiting: null,
  Done: null,
};

let WIP_LIMITS = null;
const PRIORITY_CONFIG = {
  importanceK: 0.5,
  urgencyLambda: 0.8,
  urgencyMidpointDays: 3,
};
const SNAPSHOT_REFRESH_COOLDOWN_MS = 24 * 60 * 60 * 1000;

/* -------------------- authentication middleware -------------------- */

function requireAuth(req, res, next) {
  if (!req.session.userId) {
    return res.status(401).json({ error: "Authentication required" });
  }
  next();
}

/* -------------------- file-backed stores -------------------- */

function loadJson(filePath, fallback) {
  try {
    const raw = fs.readFileSync(filePath, "utf8");
    if (!raw || raw.trim() === "") return fallback;
    return JSON.parse(raw);
  } catch (err) {
    if (err && err.code === "ENOENT") return fallback;
    const wrapped = new Error(`Could not load ${filePath}: ${err.message}`);
    wrapped.cause = err;
    throw wrapped;
  }
}

function pruneBackups(baseName, retention = 50) {
  const prefix = `${baseName}.`;
  const backups = fs
    .readdirSync(BACKUP_DIR)
    .filter((name) => name.startsWith(prefix) && name.endsWith(".bak"))
    .sort()
    .reverse();
  for (const stale of backups.slice(retention)) {
    fs.unlinkSync(path.join(BACKUP_DIR, stale));
  }
}

function snapshotJsonFile(filePath) {
  if (!fs.existsSync(filePath)) return;
  fs.mkdirSync(BACKUP_DIR, { recursive: true, mode: 0o700 });
  fs.chmodSync(BACKUP_DIR, 0o700);
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  const baseName = path.basename(filePath);
  const backupPath = path.join(
    BACKUP_DIR,
    `${baseName}.${stamp}-${crypto.randomBytes(3).toString("hex")}.bak`,
  );
  fs.copyFileSync(filePath, backupPath, fs.constants.COPYFILE_EXCL);
  fs.chmodSync(backupPath, 0o600);
  pruneBackups(baseName);
}

function saveJson(filePath, obj, { backup = true } = {}) {
  if (backup) snapshotJsonFile(filePath);
  writeFileAtomic(filePath, `${JSON.stringify(obj, null, 2)}\n`, 0o600);
}

function loadTasks() {
  const tasks = loadJson(TASKS_FILE, []);
  if (!Array.isArray(tasks)) throw new Error("tasks.json must contain an array");
  return tasks;
}
function saveTasks(tasks) {
  saveJson(TASKS_FILE, tasks);
  scheduleNextStateRefresh(tasks);
  broadcastTasksUpdate();
}

function loadUsers() {
  const users = loadJson(USERS_FILE, {});
  if (!users || Array.isArray(users) || typeof users !== "object") {
    throw new Error("users.json must contain an object");
  }
  return users;
}
function saveUsers(users) {
  saveJson(USERS_FILE, users);
}

function loadWipLimits() {
  const limits = loadJson(WIP_LIMITS_FILE, DEFAULT_WIP_LIMITS);
  if (!limits || Array.isArray(limits) || typeof limits !== "object") {
    throw new Error("wip_limits.json must contain an object");
  }
  return { ...DEFAULT_WIP_LIMITS, ...limits };
}
function saveWipLimits(limits) {
  saveJson(WIP_LIMITS_FILE, limits);
}

function transactionTarget(fileName) {
  const targets = {
    "tasks.json": TASKS_FILE,
    "users.json": USERS_FILE,
    "wip_limits.json": WIP_LIMITS_FILE,
  };
  return targets[fileName] || null;
}

function recoverPendingTransaction() {
  if (!fs.existsSync(TRANSACTION_FILE)) return false;
  const transaction = loadJson(TRANSACTION_FILE, null);
  if (
    !transaction ||
    transaction.version !== 1 ||
    !Array.isArray(transaction.entries)
  ) {
    throw new Error("Pending transaction is invalid; refusing to continue");
  }
  for (const entry of transaction.entries) {
    const filePath = transactionTarget(entry.fileName);
    if (!filePath) throw new Error(`Unknown transaction target: ${entry.fileName}`);
    saveJson(filePath, entry.data);
  }
  fs.unlinkSync(TRANSACTION_FILE);
  fsyncDirectory(DATA_DIR);
  return true;
}

function commitJsonTransaction(entries) {
  recoverPendingTransaction();
  const transaction = {
    version: 1,
    createdAt: new Date().toISOString(),
    entries: entries.map(({ filePath, data }) => ({
      fileName: path.basename(filePath),
      data,
    })),
  };
  for (const entry of transaction.entries) {
    if (!transactionTarget(entry.fileName)) {
      throw new Error(`Unsupported transaction target: ${entry.fileName}`);
    }
  }
  saveJson(TRANSACTION_FILE, transaction, { backup: false });
  for (const entry of transaction.entries) {
    saveJson(transactionTarget(entry.fileName), entry.data);
  }
  fs.unlinkSync(TRANSACTION_FILE);
  fsyncDirectory(DATA_DIR);
}

function saveTasksAndUsers(tasks, users) {
  commitJsonTransaction([
    { filePath: TASKS_FILE, data: tasks },
    { filePath: USERS_FILE, data: users },
  ]);
  scheduleNextStateRefresh(tasks);
  broadcastTasksUpdate();
}

recoverPendingTransaction();
if (!fs.existsSync(TASKS_FILE)) saveJson(TASKS_FILE, [], { backup: false });
if (!fs.existsSync(USERS_FILE)) saveJson(USERS_FILE, {}, { backup: false });
if (!fs.existsSync(WIP_LIMITS_FILE)) {
  saveJson(WIP_LIMITS_FILE, DEFAULT_WIP_LIMITS, { backup: false });
}

/* -------------------- user management helpers -------------------- */

async function hashPassword(password) {
  return await bcrypt.hash(password, 10);
}

async function verifyPassword(password, hash) {
  return await bcrypt.compare(password, hash);
}

function createUser(username, hashedPassword) {
  return {
    username,
    password: hashedPassword,
    created_at: new Date().toISOString(),
  };
}

function countLoginUsers(users) {
  return Object.values(users).filter(
    (user) => user && typeof user.password === "string" && user.password,
  ).length;
}

function validateCredentials(usernameValue, passwordValue, { login = false } = {}) {
  if (typeof usernameValue !== "string" || typeof passwordValue !== "string") {
    return { error: "Username and password required" };
  }
  const username = usernameValue.trim();
  const password = passwordValue;
  if (!username || !password) return { error: "Username and password required" };
  if (login && (username.length > 64 || password.length > 200)) {
    return { error: "Invalid username or password" };
  }
  if (!login) {
    if (!/^[A-Za-z0-9_.-]{3,32}$/.test(username)) {
      return {
        error:
          "Username must be 3-32 characters using letters, numbers, dots, dashes, or underscores",
      };
    }
    if (password.length < 10 || password.length > 200) {
      return { error: "Password must be between 10 and 200 characters" };
    }
  }
  return { username, password };
}

const DUMMY_PASSWORD_HASH = bcrypt.hashSync(
  `kanban-dummy-${crypto.randomBytes(16).toString("hex")}`,
  10,
);

function establishSession(req, username) {
  return new Promise((resolve, reject) => {
    req.session.regenerate((regenerateError) => {
      if (regenerateError) return reject(regenerateError);
      req.session.userId = username;
      req.session.save((saveError) => {
        if (saveError) return reject(saveError);
        resolve();
      });
    });
  });
}

const authAttempts = new Map();
const AUTH_WINDOW_MS = 15 * 60 * 1000;
const AUTH_MAX_ATTEMPTS = 20;

function checkAuthRateLimit(req, res) {
  const key = req.ip || req.socket.remoteAddress || "unknown";
  const now = Date.now();
  const recent = (authAttempts.get(key) || []).filter(
    (timestamp) => now - timestamp < AUTH_WINDOW_MS,
  );
  if (recent.length >= AUTH_MAX_ATTEMPTS) {
    res.set("Retry-After", String(Math.ceil(AUTH_WINDOW_MS / 1000)));
    res.status(429).json({ error: "Too many authentication attempts" });
    return null;
  }
  recent.push(now);
  authAttempts.set(key, recent);
  return key;
}

function clearAuthAttempts(key) {
  if (key) authAttempts.delete(key);
}

/* -------------------- mutation queue -------------------- */

const mutationQueue = [];
let mutationProcessing = false;

function enqueueMutation(mutFn) {
  return new Promise((resolve, reject) => {
    mutationQueue.push({ mutFn, resolve, reject });
    if (!mutationProcessing) processMutationQueue();
  });
}

async function processMutationQueue() {
  mutationProcessing = true;
  while (mutationQueue.length) {
    const { mutFn, resolve, reject } = mutationQueue.shift();
    try {
      const result = await mutFn();
      resolve(result);
    } catch (err) {
      if (!err?.status) console.error("Mutation error:", err);
      reject(err);
    }
  }
  mutationProcessing = false;
}

/* -------------------- utilities -------------------- */

function genId() {
  return crypto.randomUUID();
}

/* -------------------- effective state helpers -------------------- */

function syncSuspendedStateFromDependencies(task, allTasks = []) {
  if (!task || task.state === "Done") return;

  const hasUnresolvedDeps = anyDependencyUnresolved(allTasks, task);
  if (hasUnresolvedDeps) {
    task.state = "Suspended";
    return;
  }

  if (
    task.state === "Suspended" &&
    !(task.recurrence && task.recurrence.paused)
  ) {
    task.state = "Ready";
  }
}

/* -------------------- user points -------------------- */

function awardPoints(users, userKey, points, reason = "", entryMeta = {}) {
  if (!userKey) return null;
  const key = String(userKey).trim();
  if (!key) return null;
  if (!users[key]) users[key] = { id: key, name: key, points: 0, history: [] };
  const u = users[key];
  const pts = Math.max(0, Math.round(points || 0));
  u.points = (u.points || 0) + pts;
  u.history = u.history || [];
  const entry = {
    ts: entryMeta.ts || new Date().toISOString(),
    points: pts,
    reason,
  };
  if (entryMeta.completionId) {
    entry.completionId = entryMeta.completionId;
  }
  u.history.push(entry);
  return u;
}

function cloneJson(value) {
  return JSON.parse(JSON.stringify(value));
}

function setBlockNote(task, note) {
  const text = typeof note === "string" ? note.trim() : "";
  if (text) {
    task.meta = task.meta || {};
    task.meta.block_note = text;
    return;
  }
  if (task.meta && "block_note" in task.meta) {
    delete task.meta.block_note;
  }
}

function reverseCompletionAward(users, award) {
  if (!award || !award.user || !award.completionId) {
    return { reversed: false, points: 0 };
  }
  const user = users[award.user];
  if (!user || !Array.isArray(user.history)) {
    return { reversed: false, points: 0 };
  }
  const index = user.history.findIndex(
    (entry) => entry.completionId === award.completionId,
  );
  if (index < 0) return { reversed: false, points: 0 };
  const [entry] = user.history.splice(index, 1);
  const points = Math.max(0, Math.round(Number(entry.points) || 0));
  user.points = Math.max(0, (Number(user.points) || 0) - points);
  return { reversed: true, points };
}

function restoreTaskCompletion(task, tasks, undoneAtIso) {
  const undo = task && task.completionUndo;
  if (!undo || !undo.previousTask || !undo.completedAt) {
    throw new Error("No completion is available to undo");
  }
  if (task.updated_at !== undo.completedAt) {
    throw new Error("Task changed after completion and can no longer be undone");
  }

  const award = undo.award || null;
  const previousTask = cloneJson(undo.previousTask);
  const releasedDependents = Array.isArray(undo.releasedDependents)
    ? undo.releasedDependents
    : [];

  for (const key of Object.keys(task)) delete task[key];
  Object.assign(task, previousTask, { updated_at: undoneAtIso });

  for (const released of releasedDependents) {
    const dependent = tasks.find((candidate) => candidate.id === released.id);
    if (
      dependent &&
      dependent.state === "Ready" &&
      dependent.updated_at === released.releasedAt
    ) {
      dependent.state = released.previousState;
      if (released.previousUpdatedAt === undefined) {
        delete dependent.updated_at;
      } else {
        dependent.updated_at = released.previousUpdatedAt;
      }
    }
  }

  return { award };
}

/* -------------------- WIP helpers -------------------- */

function wouldExceedWip(
  tasks,
  targetState,
  excludeTaskId = null,
  now = new Date(),
  limitsOverride = null,
) {
  const limits = limitsOverride || WIP_LIMITS || loadWipLimits();
  const limit = limits[targetState];
  if (!Number.isFinite(limit)) return false;
  const count = tasks.filter(
    (t) =>
      t.id !== excludeTaskId &&
      computeEffectiveState(t, tasks, now).effectiveState === targetState,
  ).length;
  return count + 1 > limit;
}

function clearTaskClaim(task, tsIso = new Date().toISOString()) {
  if (!task) return false;
  const hadClaim =
    (task.picker !== null && task.picker !== undefined) ||
    (task.picked_at !== null && task.picked_at !== undefined);
  task.picker = null;
  task.picked_at = undefined;
  if (hadClaim) {
    task.unclaimed_at = tsIso;
  }
  return hadClaim;
}

function clearClaimUnlessClaimRetained(task, tasks, now = new Date()) {
  const effective = computeEffectiveState(task, tasks, now).effectiveState;
  if (effective === "InProgress" || effective === "Done") return false;
  const cleared = clearTaskClaim(task, now.toISOString());
  if (cleared && task.state === "InProgress") {
    task.state = "Ready";
  }
  return cleared;
}

function requestError(message, status = 400) {
  return { status, body: { error: message } };
}

function normalizeText(value, field, maxLength, { allowEmpty = true } = {}) {
  if (typeof value !== "string") throw requestError(`${field} must be text`);
  const text = value.trim();
  if (!allowEmpty && !text) throw requestError(`${field} is required`);
  if (text.length > maxLength) {
    throw requestError(`${field} must be ${maxLength} characters or fewer`);
  }
  return text;
}

function normalizeOptionalDate(value, field) {
  if (value === null || value === undefined || value === "") return null;
  if (typeof value !== "string") throw requestError(`${field} must be a date`);
  const parsed = parseDateSafe(value);
  if (!parsed) throw requestError(`${field} is not a valid date`);
  return parsed.toISOString();
}

function normalizeNonNegativeNumber(value, field, { integer = false } = {}) {
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0 || (integer && !Number.isInteger(number))) {
    throw requestError(
      `${field} must be a non-negative${integer ? " integer" : " number"}`,
    );
  }
  return number;
}

function normalizeBoolean(value, field) {
  if (typeof value !== "boolean") throw requestError(`${field} must be true or false`);
  return value;
}

function normalizeRecurrence(value, current = null) {
  if (!value || Array.isArray(value) || typeof value !== "object") {
    throw requestError("recurrence must be an object");
  }
  if (value.type === "none") return null;
  const recurrence = { ...(current || {}), ...value };
  if (!["rolling", "anchored"].includes(recurrence.type)) {
    throw requestError("recurrence type must be rolling, anchored, or none");
  }
  if ("leadTimeDays" in recurrence) {
    recurrence.leadTimeDays = normalizeNonNegativeNumber(
      recurrence.leadTimeDays,
      "recurrence lead time",
    );
  }
  if ("paused" in recurrence) {
    recurrence.paused = normalizeBoolean(recurrence.paused, "recurrence paused");
  }
  if ("intervalDays" in recurrence) {
    const intervalDays = Number(recurrence.intervalDays);
    if (!Number.isInteger(intervalDays) || intervalDays <= 0) {
      throw requestError("recurrence interval must be a positive integer");
    }
    recurrence.intervalDays = intervalDays;
  }
  if (recurrence.type === "anchored" && "weekdays" in recurrence) {
    if (!Array.isArray(recurrence.weekdays)) {
      throw requestError("recurrence weekdays must be an array");
    }
    recurrence.weekdays = [...new Set(recurrence.weekdays.map(Number))];
    if (
      recurrence.weekdays.some(
        (weekday) => !Number.isInteger(weekday) || weekday < 0 || weekday > 6,
      )
    ) {
      throw requestError("recurrence weekdays must be integers from 0 through 6");
    }
  } else {
    delete recurrence.weekdays;
  }
  if (!recurrence.intervalDays && !recurrence.weekdays?.length) {
    throw requestError("recurrence requires an interval or at least one weekday");
  }
  return recurrence;
}

function normalizeDependencyIds(value, tasks, taskId = null) {
  if (!Array.isArray(value)) throw requestError("dependencies must be an array");
  if (value.length > 100) throw requestError("A task cannot have more than 100 dependencies");
  const dependencies = [...new Set(value)];
  for (const dependencyId of dependencies) {
    if (typeof dependencyId !== "string" || !dependencyId.trim()) {
      throw requestError("Every dependency must be a task ID");
    }
    if (dependencyId === taskId) throw requestError("Task cannot depend on itself");
    if (!tasks.some((task) => task.id === dependencyId)) {
      throw requestError(`Dependency task ${dependencyId} not found`);
    }
  }
  return dependencies;
}

/* -------------------- dependency / cycle helpers -------------------- */

function wouldCreateCycle(tasks, taskId, depId) {
  if (taskId === depId) return true;
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const seen = new Set();
  const stack = [depId];
  while (stack.length) {
    const id = stack.pop();
    if (id === taskId) return true;
    if (seen.has(id)) continue;
    seen.add(id);
    const t = byId.get(id);
    if (!t || !Array.isArray(t.dependencies)) continue;
    for (const d of t.dependencies) {
      if (!seen.has(d)) stack.push(d);
    }
  }
  return false;
}

/* -------------------- priority engine -------------------- */

function buildPriorityContext(tasks) {
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const dependents = new Map();
  for (const t of tasks) dependents.set(t.id, []);
  for (const t of tasks) {
    if (t.state === "Done") continue;
    for (const dep of t.dependencies || []) {
      if (dependents.has(dep)) dependents.get(dep).push(t.id);
    }
  }

  const inDegree = new Map();
  for (const t of tasks) inDegree.set(t.id, 0);
  for (const t of tasks) {
    if (t.state === "Done") continue;
    for (const dep of t.dependencies || []) {
      if (!byId.has(dep)) {
        console.warn(`Task ${t.id} depends on missing task ${dep}`);
        continue;
      }
      inDegree.set(t.id, (inDegree.get(t.id) || 0) + 1);
    }
  }

  const q = [];
  for (const [id, deg] of inDegree.entries()) {
    if (deg === 0) q.push(id);
  }

  const topo = [];
  while (q.length) {
    const id = q.shift();
    topo.push(id);
    for (const childId of dependents.get(id) || []) {
      if (!inDegree.has(childId)) continue;
      inDegree.set(childId, inDegree.get(childId) - 1);
      if (inDegree.get(childId) === 0) q.push(childId);
    }
  }

  const topoSet = new Set(topo);
  const cycleNodes = new Set(
    tasks.filter((t) => !topoSet.has(t.id)).map((t) => t.id),
  );

  return {
    byId,
    dependents,
    cycleNodes,
    rawImportanceMemo: new Map(),
  };
}

function computeRawImportance(task, context, visiting = new Set()) {
  if (!task || !context) return 0;
  if (context.rawImportanceMemo.has(task.id)) {
    return context.rawImportanceMemo.get(task.id);
  }
  if (context.cycleNodes.has(task.id) || visiting.has(task.id)) {
    context.rawImportanceMemo.set(task.id, 0);
    return 0;
  }

  visiting.add(task.id);

  let rawImportance = 0;
  for (const dependentId of context.dependents.get(task.id) || []) {
    const dependent = context.byId.get(dependentId);
    if (!dependent) continue;
    rawImportance += 1 + 0.5 * computeRawImportance(dependent, context, visiting);
  }

  visiting.delete(task.id);
  context.rawImportanceMemo.set(task.id, rawImportance);
  return rawImportance;
}

function computeImportanceScore(rawImportance, k = PRIORITY_CONFIG.importanceK) {
  const R = Number(rawImportance);
  const tuning = Number(k);
  if (!(R > 0) || !(tuning > 0)) return 0;
  return (49.5 * R) / (R + tuning);
}

function computeUrgency(
  daysUntilDue,
  lambda = PRIORITY_CONFIG.urgencyLambda,
  d0 = PRIORITY_CONFIG.urgencyMidpointDays,
) {
  if (!Number.isFinite(daysUntilDue)) return 0;
  const steepness = Number(lambda);
  const midpoint = Number(d0);
  if (!(steepness > 0) || !Number.isFinite(midpoint)) return 0;
  return 49.5 / (1 + Math.exp(steepness * (daysUntilDue - midpoint)));
}

function deriveDaysUntilDue(task, now = new Date()) {
  const dueAt = parseDateSafe(task.deadline || task.scheduledDueAt);
  if (!dueAt) return null;
  return (dueAt.getTime() - now.getTime()) / (1000 * 60 * 60 * 24);
}

function computePriority(task, context, now = new Date(), config = {}) {
  const k = config.importanceK ?? PRIORITY_CONFIG.importanceK;
  const lambda = config.urgencyLambda ?? PRIORITY_CONFIG.urgencyLambda;
  const d0 = config.urgencyMidpointDays ?? PRIORITY_CONFIG.urgencyMidpointDays;
  const rawImportance = computeRawImportance(task, context);
  const importance = computeImportanceScore(rawImportance, k);
  const daysUntilDue = deriveDaysUntilDue(task, now);
  const urgency =
    daysUntilDue === null ? 0 : computeUrgency(daysUntilDue, lambda, d0);
  const priority = Math.round(1 + importance + urgency);

  return {
    rawImportance,
    importance,
    daysUntilDue,
    urgency,
    priority,
    deadlock: context.cycleNodes.has(task.id),
  };
}

function computePriorities(tasks, nowIso = new Date(), config = {}) {
  const cfg = {
    importanceK: config.importanceK ?? PRIORITY_CONFIG.importanceK,
    urgencyLambda: config.urgencyLambda ?? PRIORITY_CONFIG.urgencyLambda,
    urgencyMidpointDays:
      config.urgencyMidpointDays ?? PRIORITY_CONFIG.urgencyMidpointDays,
  };
  const now = nowIso instanceof Date ? nowIso : new Date(nowIso);
  const context = buildPriorityContext(tasks);

  return tasks.map((t) => {
    const metrics = computePriority(t, context, now, cfg);

    return Object.assign({}, t, {
      importanceRaw: metrics.rawImportance,
      importance: metrics.importance,
      urgency: metrics.urgency,
      priority: metrics.priority,
      deadlock: metrics.deadlock,
    });
  });
}

/* -------------------- recompute wrapper -------------------- */

function recomputeAllPriorities(tasks = null) {
  const shouldPersist = tasks === null;
  const ts = tasks ?? loadTasks();
  const updated = computePriorities(ts, new Date(), PRIORITY_CONFIG);
  const changed = [];
  for (const u of updated) {
    const t = ts.find((x) => x.id === u.id);
    if (!t) continue;
    const before = {
      priority: t.priority,
      urgency: t.urgency,
      importance: t.importance,
      importanceRaw: t.importanceRaw,
    };
    t.priority = u.priority;
    t.urgency = u.urgency;
    t.importance = u.importance;
    t.importanceRaw = u.importanceRaw;
    t.deadlock = u.deadlock;
    delete t.importancePercentile;
    delete t.recurrenceCrowding;
    delete t.overdueAge;
    if (
      before.priority !== t.priority ||
      before.urgency !== t.urgency ||
      before.importance !== t.importance ||
      before.importanceRaw !== t.importanceRaw
    ) {
      changed.push({
        id: t.id,
        before,
        after: {
          priority: t.priority,
          urgency: t.urgency,
          importance: t.importance,
          importanceRaw: t.importanceRaw,
        },
      });
    }
  }
  if (changed.length && process.env.KANBAN_DEBUG === "true") {
    console.log("Priorities/metrics changed:", changed);
  }
  if (shouldPersist) saveTasks(ts);
  return ts;
}

/* -------------------- Authentication API -------------------- */

app.post("/auth/register", async (req, res) => {
  const rateLimitKey = checkAuthRateLimit(req, res);
  if (!rateLimitKey) return;
  try {
    const credentials = validateCredentials(req.body.username, req.body.password);
    if (credentials.error) {
      return res.status(400).json({ error: credentials.error });
    }
    const { username, password } = credentials;
    const hashedPassword = await hashPassword(password);
    await enqueueMutation(async () => {
      const users = loadUsers();
      if (
        countLoginUsers(users) > 0 &&
        process.env.ALLOW_REGISTRATION !== "true" &&
        !req.session.userId
      ) {
        throw {
          status: 403,
          body: { error: "Registration is disabled" },
        };
      }
      if (users[username] && users[username].password) {
        throw {
          status: 409,
          body: { error: "Username already exists" },
        };
      }
      users[username] = {
        ...(users[username] || {}),
        ...createUser(username, hashedPassword),
      };
      saveUsers(users);
    });

    await establishSession(req, username);
    clearAuthAttempts(rateLimitKey);
    res.json({ success: true, username });
  } catch (err) {
    if (err && err.status && err.body) {
      return res.status(err.status).json(err.body);
    }
    console.error("Register error:", err);
    res.status(500).json({ error: "Registration failed" });
  }
});

app.post("/auth/login", async (req, res) => {
  const rateLimitKey = checkAuthRateLimit(req, res);
  if (!rateLimitKey) return;
  try {
    const credentials = validateCredentials(req.body.username, req.body.password, {
      login: true,
    });
    if (credentials.error) {
      return res.status(400).json({ error: credentials.error });
    }
    const { username, password } = credentials;

    const users = loadUsers();
    const user = users[username];
    const valid = await verifyPassword(
      password,
      user && typeof user.password === "string"
        ? user.password
        : DUMMY_PASSWORD_HASH,
    );

    if (!user || !valid) {
      return res.status(401).json({ error: "Invalid username or password" });
    }

    await establishSession(req, username);
    clearAuthAttempts(rateLimitKey);
    res.json({ success: true, username });
  } catch (err) {
    console.error("Login error:", err);
    res.status(500).json({ error: "Login failed" });
  }
});

app.post("/auth/change-password", requireAuth, async (req, res) => {
  const rateLimitKey = checkAuthRateLimit(req, res);
  if (!rateLimitKey) return;

  const currentPassword = req.body.currentPassword;
  const newPassword = req.body.newPassword;
  if (
    typeof currentPassword !== "string" ||
    !currentPassword ||
    currentPassword.length > 200
  ) {
    return res.status(400).json({ error: "Current password is required" });
  }
  if (
    typeof newPassword !== "string" ||
    newPassword.length < 10 ||
    newPassword.length > 200
  ) {
    return res
      .status(400)
      .json({ error: "New password must be between 10 and 200 characters" });
  }
  if (currentPassword === newPassword) {
    return res.status(400).json({ error: "New password must be different" });
  }

  const username = req.session.userId;
  try {
    await enqueueMutation(async () => {
      const users = loadUsers();
      const user = users[username];
      const valid = await verifyPassword(
        currentPassword,
        user && typeof user.password === "string"
          ? user.password
          : DUMMY_PASSWORD_HASH,
      );
      if (!user || !valid) {
        throw requestError("Current password is incorrect", 401);
      }
      user.password = await hashPassword(newPassword);
      user.password_changed_at = new Date().toISOString();
      saveUsers(users);
    });

    await new Promise((resolve, reject) => {
      sessionStore.destroyByUserId(username, (error) =>
        error ? reject(error) : resolve(),
      );
    });
    closeUserSockets(username);
    await establishSession(req, username);
    clearAuthAttempts(rateLimitKey);
    res.json({ success: true, username });
  } catch (error) {
    if (error?.status && error?.body) {
      return res.status(error.status).json(error.body);
    }
    console.error("Change password error:", error);
    res.status(500).json({ error: "Password change failed" });
  }
});

app.post("/auth/logout", (req, res) => {
  const sessionId = req.sessionID;
  req.session.destroy((err) => {
    if (err) {
      console.error("Logout error:", err);
      return res.status(500).json({ error: "Logout failed" });
    }
    closeSessionSockets(sessionId);
    res.json({ success: true });
  });
});

app.get("/auth/whoami", (req, res) => {
  if (req.session.userId) {
    const users = loadUsers();
    const user = users[req.session.userId];
    if (user) {
      res.json({
        authenticated: true,
        username: user.username,
        created_at: user.created_at,
        points: user.points || 0,
        completionHistory: (user.history || []).filter((entry) =>
          String(entry.reason || "").startsWith("Completed task "),
        ),
      });
    } else {
      req.session.destroy();
      res.json({ authenticated: false });
    }
  } else {
    res.json({ authenticated: false });
  }
});

app.get("/auth/registration-status", (req, res) => {
  try {
    const users = loadUsers();
    res.json({
      enabled:
        countLoginUsers(users) === 0 ||
        process.env.ALLOW_REGISTRATION === "true" ||
        Boolean(req.session.userId),
    });
  } catch (error) {
    console.error("Registration status error:", error);
    res.status(500).json({ error: "Could not check registration status" });
  }
});

/* -------------------- API handlers -------------------- */

app.get("/healthz", (req, res) => {
  try {
    loadTasks();
    loadUsers();
    loadWipLimits();
    res.json({ status: "ok" });
  } catch (error) {
    console.error("Health check failed:", error);
    res.status(503).json({ status: "error" });
  }
});

app.get("/", (req, res) => res.send("Server running."));

function enrichTasksForClient(tasks, now = new Date()) {
  return computePriorities(tasks, now, PRIORITY_CONFIG).map((task) => {
    const effective = computeEffectiveState(task, tasks, now);
    const publicTask = {
      ...task,
      effectiveState: effective.effectiveState,
      readyAt: effective.readyAt || null,
      scheduledDueAt: effective.scheduledDueAt || null,
      overdue: Boolean(effective.overdue),
    };
    delete publicTask.points_history;
    if (task.completionUndo) {
      publicTask.completionUndo = {
        available: true,
        completedAt: task.completionUndo.completedAt,
      };
    }
    return publicTask;
  });
}

app.get("/tasks", requireAuth, (req, res) => {
  try {
    const tasks = loadTasks();
    res.json(enrichTasksForClient(tasks));
  } catch (err) {
    console.error("GET /tasks error while enriching:", err);
    res.status(500).json({ error: "Internal error fetching tasks" });
  }
});

app.post("/tasks", requireAuth, async (req, res) => {
  try {
    const created = await enqueueMutation(async () => {
      const tasks = loadTasks();

      const dependencies = normalizeDependencyIds(
        req.body.dependencies || [],
        tasks,
      );
      const title = normalizeText(
        req.body.title || "Untitled Task",
        "title",
        200,
        { allowEmpty: false },
      );
      const description = normalizeText(
        req.body.description || "",
        "description",
        20000,
      );
      const scheduledDueAt = normalizeOptionalDate(
        req.body.scheduledDueAt,
        "scheduled due date",
      );
      const deadline = normalizeOptionalDate(req.body.deadline, "deadline");

      const newTask = {
        id: genId(),
        title,
        description,
        state: "Ready",
        deadline: deadline || undefined,
        scheduledDueAt: scheduledDueAt || undefined,
        dependencies,
        picker: null,
        points_snapshot: undefined,
        picked_at: undefined,
        awarded: undefined,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        created_by: req.session.userId,
        meta: {},
        timeCritical:
          "timeCritical" in req.body
            ? normalizeBoolean(req.body.timeCritical, "timeCritical")
            : false,
      };

      if (req.body.recurrence) {
        const recurrence = normalizeRecurrence(req.body.recurrence);
        if (recurrence) newTask.recurrence = recurrence;
      }
      if ("leadTimeDays" in req.body) {
        newTask.leadTimeDays = normalizeNonNegativeNumber(
          req.body.leadTimeDays,
          "lead time",
        );
      }

      tasks.push(newTask);
      syncSuspendedStateFromDependencies(newTask, tasks);

      recomputeAllPriorities(tasks);
      saveTasks(tasks);

      return enrichTasksForClient(tasks).find((task) => task.id === newTask.id);
    });
    res.status(201).json(created);
  } catch (err) {
    console.error("POST /tasks error:", err);
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    res.status(500).json({ error: "Internal error creating task" });
  }
});

/* PATCH state handler — unchanged except recurrence-advancement remains on completion */
app.patch("/tasks/:id/state", requireAuth, async (req, res) => {
  try {
    const result = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const task = tasks.find((t) => t.id === req.params.id);
      if (!task) throw { status: 404, body: { error: "Task not found" } };

      const newState = req.body.state;
      if (!TASK_STATES.includes(newState)) {
        throw requestError(`state must be one of: ${TASK_STATES.join(", ")}`);
      }
      if ("picker" in req.body) {
        throw requestError("picker is controlled by the server");
      }
      const note =
        newState === "Blocked"
          ? normalizeText(req.body.note || "", "block note", 5000)
          : "";

      const eff = computeEffectiveState(task, tasks, new Date());

      if (newState === "InProgress") {
        if (eff.effectiveState !== "Ready") {
          throw {
            status: 400,
            body: {
              error: `Not actionable yet; ready at ${eff.readyAt || eff.scheduledDueAt || "unknown"}`,
            },
          };
        }
      }
      if (newState === "Done") {
        if (
          task.state !== "InProgress" &&
          eff.effectiveState !== "Ready"
        ) {
          throw {
            status: 400,
            body: {
              error: `Cannot complete yet; ready at ${eff.readyAt || eff.scheduledDueAt || "unknown"}`,
            },
          };
        }
      }

      if (wouldExceedWip(tasks, newState, task.id)) {
        throw {
          status: 400,
          body: {
            error: `WIP limit exceeded for ${newState}. Limit: ${WIP_LIMITS[newState]}`,
          },
        };
      }

      function persistAndReturn() {
        recomputeAllPriorities(tasks);
        saveTasks(tasks);
        return enrichTasksForClient(tasks).find((candidate) => candidate.id === task.id);
      }

      if (newState === "InProgress") {
        const claimTs = new Date();
        const claimTsIso = claimTs.toISOString();
        delete task.completionUndo;
        task.state = "InProgress";
        setBlockNote(task, "");
        task.picker = req.session.userId;
        const snapshotOwnerRaw = task.points_snapshot_created_by;
        const snapshotOwner =
          typeof snapshotOwnerRaw === "string"
            ? snapshotOwnerRaw.trim()
            : snapshotOwnerRaw;
        const unclaimedAt = parseDateSafe(task.unclaimed_at);
        const unclaimedLongEnough =
          !!unclaimedAt &&
          claimTs.getTime() - unclaimedAt.getTime() >=
            SNAPSHOT_REFRESH_COOLDOWN_MS;
        const claimedByDifferentUser =
          task.picker !== "" && task.picker !== snapshotOwner;
        const shouldRefreshSnapshot =
          typeof task.points_snapshot !== "number" ||
          claimedByDifferentUser ||
          unclaimedLongEnough;

        if (shouldRefreshSnapshot) {
          const updated = computePriorities(tasks, claimTs, PRIORITY_CONFIG);
          const u = updated.find((x) => x.id === task.id);
          const snap =
            u && typeof u.priority === "number"
              ? u.priority
              : task.priority || 0;
          task.points_snapshot = snap;
          task.points_snapshot_created_at = claimTsIso;
          task.points_snapshot_created_by = task.picker;
          task.picked_at = claimTsIso;
          task.points_history = task.points_history || [];
          task.points_history.push({
            ts: task.points_snapshot_created_at,
            snapshot: snap,
            by: task.points_snapshot_created_by,
          });
        } else if (!task.picked_at) {
          task.picked_at = claimTsIso;
        }
        task.unclaimed_at = undefined;
        task.updated_at = claimTsIso;
        return persistAndReturn();
      }

      if (newState === "Blocked") {
        delete task.completionUndo;
        task.state = "Blocked";
        setBlockNote(task, note);
        const updatedAtIso = new Date().toISOString();
        clearClaimUnlessClaimRetained(task, tasks, new Date(updatedAtIso));
        task.updated_at = updatedAtIso;
        return persistAndReturn();
      }

      if (newState === "Done") {
        if (anyDependencyUnresolved(tasks, task)) {
          throw {
            status: 400,
            body: { error: "Cannot complete task: dependencies not done" },
          };
        }

        const wasBlocked = task.state === "Blocked";
        const pointsToAward =
          typeof task.points_snapshot === "number"
            ? Math.max(0, Math.round(task.points_snapshot))
            : 0;
        const pickerKeyRaw = task.picker;
        const pickerKey =
          typeof pickerKeyRaw === "string" ? pickerKeyRaw.trim() : pickerKeyRaw;
        const users = pickerKey ? loadUsers() : null;

        const completedAtIso = new Date().toISOString();
        const completionId = `${task.id}:${completedAtIso}`;
        const previousTask = cloneJson(task);
        delete previousTask.completionUndo;
        const completionUndo = {
          version: 1,
          completionId,
          completedAt: completedAtIso,
          previousTask,
          releasedDependents: [],
          award: null,
        };
        task.lastCompletedAt = completedAtIso;
        task.state = "Done";
        task.updated_at = completedAtIso;
        setBlockNote(task, "");

        tasks.forEach((t) => {
          if (
            t.state === "Suspended" &&
            (t.dependencies || []).includes(task.id)
          ) {
            const allDepsDone = !anyDependencyUnresolved(tasks, t);
            if (allDepsDone) {
              completionUndo.releasedDependents.push({
                id: t.id,
                previousState: t.state,
                previousUpdatedAt: t.updated_at,
                releasedAt: completedAtIso,
              });
              t.state = "Ready";
              t.updated_at = completedAtIso;
            }
          }
        });

        if (pickerKey) {
          const awardedPoints = wasBlocked ? 0 : pointsToAward;
          awardPoints(
            users,
            pickerKey,
            awardedPoints,
            `Completed task ${task.id} (${task.title})`,
            { ts: completedAtIso, completionId },
          );
          completionUndo.award = {
            user: pickerKey,
            points: awardedPoints,
            completionId,
          };
          task.awarded = {
            to: pickerKey,
            points: awardedPoints,
            reason: wasBlocked ? "blocked" : undefined,
            ts: completedAtIso,
          };
          task.points_snapshot_awarded = awardedPoints > 0;
        } else {
          task.awarded = {
            to: null,
            points: 0,
            reason: wasBlocked ? "blocked" : "none",
            ts: new Date().toISOString(),
          };
          task.points_snapshot_awarded = false;
        }

        // recurrence advancement only if recurrence exists
        if (task.recurrence && typeof task.recurrence === "object") {
          const r = task.recurrence;
          if (
            r.type === "rolling" &&
            Number.isFinite(Number(r.intervalDays)) &&
            Number(r.intervalDays) > 0
          ) {
            const intervalDays = Number(r.intervalDays);
            const msInterval = intervalDays * 24 * 60 * 60 * 1000;
            const next = new Date(Date.parse(completedAtIso) + msInterval);
            task.scheduledDueAt = next.toISOString();
            task.state = "Ready";
            task.picker = null;
            task.picked_at = undefined;
            task.points_snapshot = undefined;
            task.points_snapshot_created_at = undefined;
          } else if (r.type === "anchored") {
            if (Array.isArray(r.weekdays) && r.weekdays.length) {
              // Anchored to specific weekdays
              const wds = r.weekdays
                .map((n) => Number(n))
                .filter((x) => Number.isFinite(x));
              const preserve = parseDateSafe(task.scheduledDueAt) || new Date();
              let d = new Date(completedAtIso);
              d.setHours(
                preserve.getHours(),
                preserve.getMinutes(),
                preserve.getSeconds(),
                preserve.getMilliseconds(),
              );
              for (let i = 1; i <= 14; ++i) {
                d.setDate(d.getDate() + 1);
                if (wds.includes(d.getDay())) {
                  task.scheduledDueAt = d.toISOString();
                  task.state = "Ready";
                  task.picker = null;
                  task.picked_at = undefined;
                  task.points_snapshot = undefined;
                  task.points_snapshot_created_at = undefined;
                  break;
                }
              }
            } else if (
              Number.isFinite(Number(r.intervalDays)) &&
              Number(r.intervalDays) > 0
            ) {
              // Anchored by interval (like rolling but preserves time)
              const intervalDays = Number(r.intervalDays);
              const preserve = parseDateSafe(task.scheduledDueAt) || new Date();
              const msInterval = intervalDays * 24 * 60 * 60 * 1000;
              const next = new Date(preserve.getTime() + msInterval);
              task.scheduledDueAt = next.toISOString();
              task.state = "Ready";
              task.picker = null;
              task.picked_at = undefined;
              task.points_snapshot = undefined;
              task.points_snapshot_created_at = undefined;
            }
          }
        }

        clearClaimUnlessClaimRetained(task, tasks, new Date(completedAtIso));
        task.completionUndo = completionUndo;
        recomputeAllPriorities(tasks);
        if (users) saveTasksAndUsers(tasks, users);
        else saveTasks(tasks);
        return enrichTasksForClient(tasks).find((candidate) => candidate.id === task.id);
      }

      delete task.completionUndo;
      task.state = newState;
      if (newState !== "Blocked") {
        setBlockNote(task, "");
      }
      const updatedAtIso = new Date().toISOString();
      clearClaimUnlessClaimRetained(task, tasks, new Date(updatedAtIso));
      task.updated_at = updatedAtIso;

      recomputeAllPriorities(tasks);
      saveTasks(tasks);
      return enrichTasksForClient(tasks).find((candidate) => candidate.id === task.id);
    });

    if (result && result.status && result.body) {
      return res.status(result.status).json(result.body);
    }
    res.json(result);
  } catch (err) {
    if (err && err.status && err.body) {
      return res.status(err.status).json(err.body);
    }
    console.error("PATCH /tasks/:id/state error:", err);
    res.status(500).json({ error: "Internal error changing state" });
  }
});

app.post("/tasks/:id/undo-complete", requireAuth, async (req, res) => {
  try {
    const restored = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const task = tasks.find((candidate) => candidate.id === req.params.id);
      if (!task) throw { status: 404, body: { error: "Task not found" } };

      const undo = task.completionUndo;
      if (!undo || !undo.previousTask || !undo.completedAt) {
        throw {
          status: 409,
          body: { error: "This completion is no longer available to undo" },
        };
      }
      if (task.updated_at !== undo.completedAt) {
        throw {
          status: 409,
          body: { error: "Task changed after completion and cannot be undone" },
        };
      }
      const changedDependent = (undo.releasedDependents || []).some(
        (released) => {
          const dependent = tasks.find(
            (candidate) => candidate.id === released.id,
          );
          return (
            dependent &&
            (dependent.state !== "Ready" ||
              dependent.updated_at !== released.releasedAt)
          );
        },
      );
      if (changedDependent) {
        throw {
          status: 409,
          body: {
            error: "A dependent task changed after completion and prevents undo",
          },
        };
      }

      let users = null;
      if (undo.award) {
        users = loadUsers();
        const reversal = reverseCompletionAward(users, undo.award);
        if (!reversal.reversed) {
          throw {
            status: 409,
            body: { error: "Completion points changed and cannot be reversed" },
          };
        }
      }

      restoreTaskCompletion(task, tasks, new Date().toISOString());
      recomputeAllPriorities(tasks);
      if (users) saveTasksAndUsers(tasks, users);
      else saveTasks(tasks);
      return enrichTasksForClient(tasks).find(
        (candidate) => candidate.id === task.id,
      );
    });
    res.json(restored);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("POST /tasks/:id/undo-complete error:", err);
    res.status(500).json({ error: "Internal error undoing completion" });
  }
});

app.post("/tasks/:id/remedy", requireAuth, async (req, res) => {
  try {
    const result = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const blockedTask = tasks.find((t) => t.id === req.params.id);
      if (!blockedTask)
        throw { status: 404, body: { error: "Blocked task not found" } };
      if (computeEffectiveState(blockedTask, tasks).effectiveState !== "Blocked") {
        throw {
          status: 409,
          body: { error: "A remedy can only be created for a blocked task" },
        };
      }

      const deadline = normalizeOptionalDate(
        "deadline" in req.body ? req.body.deadline : blockedTask.deadline,
        "deadline",
      );
      const scheduledDueAt = normalizeOptionalDate(
        "scheduledDueAt" in req.body
          ? req.body.scheduledDueAt
          : blockedTask.scheduledDueAt,
        "scheduled due date",
      );
      const description = normalizeText(
        req.body.description || `Remedy for: ${blockedTask.title}`,
        "description",
        20000,
      );
      const title = normalizeText(
        req.body.title || `Remedy for ${blockedTask.title}`,
        "title",
        200,
        { allowEmpty: false },
      );

      const newTask = {
        id: genId(),
        title,
        description,
        state: "Ready",
        deadline: deadline || undefined,
        scheduledDueAt: scheduledDueAt || undefined,
        dependencies: [],
        picker: null,
        points_snapshot: undefined,
        picked_at: undefined,
        awarded: undefined,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        created_by: req.session.userId,
        meta: {},
        remedy_for: req.params.id,
      };

      tasks.push(newTask);

      blockedTask.dependencies = blockedTask.dependencies || [];
      if (!blockedTask.dependencies.includes(newTask.id))
        blockedTask.dependencies.push(newTask.id);
      blockedTask.state = "Suspended";
      setBlockNote(blockedTask, "");
      const updatedAtIso = new Date().toISOString();
      clearTaskClaim(blockedTask, updatedAtIso);
      blockedTask.updated_at = updatedAtIso;

      recomputeAllPriorities(tasks);
      saveTasks(tasks);

      const enriched = enrichTasksForClient(tasks);
      return {
        blockedTask: enriched.find((task) => task.id === blockedTask.id),
        remedyTask: enriched.find((task) => task.id === newTask.id),
      };
    });

    res.json(result);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("POST /tasks/:id/remedy error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

app.delete("/tasks/:id", requireAuth, async (req, res) => {
  try {
    const confirm =
      (req.query && req.query.confirm === "true") ||
      (req.body && req.body.confirm === true) ||
      (req.get && req.get("X-Confirm-Delete") === "1");

    const result = await enqueueMutation(async () => {
      let tasks = loadTasks();
      const targetId = req.params.id;

      const exists = tasks.some((t) => t.id === targetId);
      if (!exists) throw { status: 404, body: { error: "Task not found" } };

      const incomingDependents = tasks
        .filter((task) => task.id !== targetId)
        .filter(
          (t) =>
            Array.isArray(t.dependencies) && t.dependencies.includes(targetId),
        )
        .map((t) => ({ id: t.id, title: t.title, state: t.state }));

      if (incomingDependents.length && !confirm) {
        throw {
          status: 409,
          body: {
            error:
              "Other tasks depend on this task. Confirm deletion to proceed; this will affect the dependent tasks.",
            dependents: incomingDependents,
          },
        };
      }

      tasks = tasks.filter((task) => task.id !== targetId);
      const adjusted = [];
      tasks.forEach((t) => {
        let changed = false;
        if (Array.isArray(t.dependencies)) {
          const filtered = t.dependencies.filter((dependencyId) => dependencyId !== targetId);
          if (filtered.length !== t.dependencies.length) {
            t.dependencies = filtered;
            changed = true;
          }
        }
        if (t.remedy_for === targetId) {
          delete t.remedy_for;
          changed = true;
        }

        if (changed) {
          syncSuspendedStateFromDependencies(t, tasks);
          t.updated_at = new Date().toISOString();
          adjusted.push({ id: t.id, title: t.title, state: t.state });
        }
      });

      recomputeAllPriorities(tasks);
      saveTasks(tasks);

      return {
        message: "Deleted",
        deleted: [targetId],
        removed_count: 1,
        adjusted,
      };
    });

    res.json(result);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("DELETE /tasks/:id error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

/* PATCH /tasks/:id edits. Important: support recurrence type 'none' */
app.patch("/tasks/:id", requireAuth, async (req, res) => {
  try {
    const updated = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const task = tasks.find((t) => t.id === req.params.id);
      if (!task) throw { status: 404, body: { error: "Task not found" } };
      if (typeof req.body.expectedUpdatedAt !== "string") {
        throw requestError("expectedUpdatedAt is required", 428);
      }
      if (task.updated_at !== req.body.expectedUpdatedAt) {
        throw requestError(
          "Task changed since it was opened. Reload it before saving.",
          409,
        );
      }

      delete task.completionUndo;

      if ("title" in req.body) {
        task.title = normalizeText(req.body.title, "title", 200, {
          allowEmpty: false,
        });
      }
      if ("description" in req.body) {
        task.description = normalizeText(
          req.body.description,
          "description",
          20000,
        );
      }
      if ("scheduledDueAt" in req.body) {
        task.scheduledDueAt = normalizeOptionalDate(
          req.body.scheduledDueAt,
          "scheduled due date",
        );
      }
      if ("leadTimeDays" in req.body) {
        task.leadTimeDays = normalizeNonNegativeNumber(
          req.body.leadTimeDays,
          "lead time",
        );
      }
      if ("timeCritical" in req.body) {
        task.timeCritical = normalizeBoolean(req.body.timeCritical, "timeCritical");
      }
      if ("blockNote" in req.body) {
        const blockNote = normalizeText(
          req.body.blockNote,
          "block note",
          5000,
        );
        if (blockNote && computeEffectiveState(task, tasks).effectiveState !== "Blocked") {
          throw requestError("Only blocked tasks can have a block note");
        }
        setBlockNote(task, blockNote);
      }

      let recurrenceEdited = false;
      if ("recurrence" in req.body) {
        const previousRecurrence = JSON.stringify(task.recurrence || null);
        const recurrence = normalizeRecurrence(
          req.body.recurrence,
          task.recurrence,
        );
        if (!recurrence) {
          delete task.recurrence;
        } else {
          task.recurrence = recurrence;
          delete task.leadTimeDays;
        }
        recurrenceEdited =
          previousRecurrence !== JSON.stringify(task.recurrence || null);
      }

      // anchored normalization only if recurrence edited and anchored selected
      if (
        recurrenceEdited &&
        task.recurrence &&
        task.recurrence.type === "anchored" &&
        Array.isArray(task.recurrence.weekdays) &&
        task.recurrence.weekdays.length
      ) {
        const now = new Date();
        const wds = task.recurrence.weekdays
          .map((n) => Number(n))
          .filter((x) => Number.isFinite(x));
        const preserve = parseDateSafe(task.scheduledDueAt) || null;
        const baseTime = preserve || now;
        let d = new Date(
          now.getFullYear(),
          now.getMonth(),
          now.getDate(),
          baseTime.getHours(),
          baseTime.getMinutes(),
          baseTime.getSeconds(),
          baseTime.getMilliseconds(),
        );
        for (let i = 0; i < 14; ++i) {
          d.setDate(d.getDate() + 1);
          if (wds.includes(d.getDay())) {
            task.scheduledDueAt = d.toISOString();
            break;
          }
        }
      }

      // Handle dependencies with cycle detection
      if ("dependencies" in req.body) {
        const newDeps = normalizeDependencyIds(
          req.body.dependencies,
          tasks,
          task.id,
        );

        for (const depId of newDeps) {
          const depTask = tasks.find((t) => t.id === depId);
          if (wouldCreateCycle(tasks, task.id, depId)) {
            throw {
              status: 400,
              body: {
                error: `Adding dependency "${depTask.title}" would create a circular dependency`,
              },
            };
          }
        }

        task.dependencies = newDeps;
        syncSuspendedStateFromDependencies(task, tasks);
      }

      const updatedAtIso = new Date().toISOString();
      clearClaimUnlessClaimRetained(task, tasks, new Date(updatedAtIso));
      task.updated_at = updatedAtIso;

      recomputeAllPriorities(tasks);
      saveTasks(tasks);
      return enrichTasksForClient(tasks).find((candidate) => candidate.id === task.id);
    });

    res.json(updated);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("PATCH /tasks/:id error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

/* -------------------- WIP limits endpoints -------------------- */

app.get("/wip-limits", requireAuth, (req, res) => {
  try {
    const limits = loadWipLimits();
    res.json(limits);
  } catch (err) {
    console.error("GET /wip-limits error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

app.patch("/wip-limits", requireAuth, async (req, res) => {
  try {
    const updated = await enqueueMutation(async () => {
      const limits = loadWipLimits();

      for (const state of TASK_STATES) {
        if (!(state in req.body)) continue;
        const value = req.body[state];
        if (value !== null && (!Number.isInteger(value) || value < 0)) {
          throw requestError(`${state} WIP limit must be a non-negative integer or null`);
        }
        limits[state] = value;
      }

      saveWipLimits(limits);
      WIP_LIMITS = limits; // Update cached limits
      return limits;
    });

    res.json(updated);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("PATCH /wip-limits error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

app.use((error, req, res, next) => {
  if (res.headersSent) return next(error);
  if (error?.type === "entity.parse.failed") {
    return res.status(400).json({ error: "Request body is not valid JSON" });
  }
  if (error?.type === "entity.too.large") {
    return res.status(413).json({ error: "Request body is too large" });
  }
  console.error("Unhandled request error:", error);
  res.status(500).json({ error: "Internal server error" });
});

/* -------------------- server & live updates -------------------- */

const PORT = process.env.PORT || 3000;
const HOST = process.env.KANBAN_HOST || "127.0.0.1";
const server = require("http").createServer(app);

const wss = new WebSocketServer({ noServer: true, maxPayload: 64 * 1024 });
const clients = new Map();

function rejectWebSocket(socket, status, message) {
  socket.write(
    `HTTP/1.1 ${status} ${message}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`,
  );
  socket.destroy();
}

server.on("upgrade", (request, socket, head) => {
  try {
    const origin = request.headers.origin;
    if (origin && new URL(origin).host !== request.headers.host) {
      return rejectWebSocket(socket, 403, "Forbidden");
    }
  } catch (error) {
    return rejectWebSocket(socket, 400, "Bad Request");
  }

  sessionMiddleware(request, {}, () => {
    try {
      const userId = request.session?.userId;
      const users = loadUsers();
      if (!userId || !users[userId]?.password) {
        return rejectWebSocket(socket, 401, "Unauthorized");
      }
      wss.handleUpgrade(request, socket, head, (webSocket) => {
        wss.emit("connection", webSocket, request);
      });
    } catch (error) {
      console.error("WebSocket authentication failed:", error);
      rejectWebSocket(socket, 500, "Internal Server Error");
    }
  });
});

wss.on("connection", (ws, request) => {
  const client = {
    sessionId: request.sessionID,
    userId: request.session.userId,
  };
  clients.set(ws, client);

  try {
    const tasks = loadTasks();
    ws.send(JSON.stringify({ type: "tasks", data: enrichTasksForClient(tasks) }));
  } catch (err) {
    console.error("Error sending initial tasks:", err);
  }

  ws.on("message", () => ws.close(1008, "Read-only WebSocket"));

  ws.on("close", () => {
    clients.delete(ws);
  });

  ws.on("error", (err) => {
    console.error("WebSocket error:", err);
    clients.delete(ws);
  });
});

function broadcastTasksUpdate() {
  if (clients.size === 0) return;

  try {
    const tasks = loadTasks();
    const message = JSON.stringify({
      type: "tasks",
      data: enrichTasksForClient(tasks),
    });
    clients.forEach((client, webSocket) => {
      sessionStore.get(client.sessionId, (error, storedSession) => {
        if (
          error ||
          !storedSession ||
          storedSession.userId !== client.userId ||
          webSocket.readyState !== 1
        ) {
          webSocket.close(1008, "Session expired");
          return;
        }
        webSocket.send(message);
      });
    });
  } catch (err) {
    console.error("Error broadcasting tasks:", err);
  }
}

function closeSessionSockets(sessionId) {
  clients.forEach((client, webSocket) => {
    if (client.sessionId === sessionId) webSocket.close(1008, "Logged out");
  });
}

function closeUserSockets(userId) {
  clients.forEach((client, webSocket) => {
    if (client.userId === userId) webSocket.close(1008, "Password changed");
  });
}

let stateRefreshTimer = null;
function scheduleNextStateRefresh(tasks = null) {
  if (stateRefreshTimer) clearTimeout(stateRefreshTimer);
  stateRefreshTimer = null;
  const currentTasks = tasks || loadTasks();
  const nowMs = Date.now();
  const nextTransitionMs = currentTasks
    .map((task) => parseDateSafe(calcReadyAt(task))?.getTime())
    .filter((timestamp) => Number.isFinite(timestamp) && timestamp > nowMs)
    .sort((a, b) => a - b)[0];
  if (!nextTransitionMs) return;
  const delay = Math.min(nextTransitionMs - nowMs + 50, 2_147_000_000);
  stateRefreshTimer = setTimeout(() => {
    broadcastTasksUpdate();
    try {
      scheduleNextStateRefresh();
    } catch (error) {
      console.error("Failed to schedule task state refresh:", error);
    }
  }, delay);
  stateRefreshTimer.unref();
}

function startServer(port = PORT, host = HOST) {
  if (server.listening) return server;
  WIP_LIMITS = loadWipLimits();
  scheduleNextStateRefresh();
  server.listen(Number(port), host, () => {
    const address = server.address();
    console.log(`Server listening on ${address.address}:${address.port}`);
  });
  return server;
}

if (require.main === module) {
  startServer();
}

module.exports = {
  PRIORITY_CONFIG,
  buildPriorityContext,
  computeRawImportance,
  computeImportanceScore,
  computeUrgency,
  deriveDaysUntilDue,
  computeEffectiveState,
  computePriority,
  computePriorities,
  recomputeAllPriorities,
  wouldExceedWip,
  clearClaimUnlessClaimRetained,
  reverseCompletionAward,
  restoreTaskCompletion,
  startServer,
};
