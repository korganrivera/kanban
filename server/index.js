// index.js (recurrence handling: none option + standard behavior)
// ... (first lines unchanged)
const express = require("express");
const app = express();
const fs = require("fs");
const path = require("path");
const { WebSocketServer } = require("ws");
const session = require("express-session");
const bcrypt = require("bcrypt");

const DATA_DIR = path.join(__dirname, "data");
if (!fs.existsSync(DATA_DIR)) fs.mkdirSync(DATA_DIR);

const TASKS_FILE = path.join(DATA_DIR, "tasks.json");
const USERS_FILE = path.join(DATA_DIR, "users.json");
const WIP_LIMITS_FILE = path.join(DATA_DIR, "wip_limits.json");

app.use(express.json());

// Session configuration
app.use(
  session({
    secret: process.env.SESSION_SECRET || "kanban-secret-change-in-production",
    resave: false,
    saveUninitialized: false,
    cookie: {
      maxAge: 1000 * 60 * 60 * 24 * 7, // 1 week
      httpOnly: true,
    },
  }),
);

// Serve static files AFTER session setup
app.use(express.static("static"));

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

/* -------------------- authentication middleware -------------------- */

function requireAuth(req, res, next) {
  if (!req.session.userId) {
    return res.status(401).json({ error: "Authentication required" });
  }
  next();
}

function optionalAuth(req, res, next) {
  // Just pass through - used for endpoints that work better with auth but don't require it
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
    console.error(`Error loading ${filePath}:`, err);
    return fallback;
  }
}

function saveJson(filePath, obj) {
  try {
    const tmp = filePath + ".tmp";
    fs.writeFileSync(tmp, JSON.stringify(obj, null, 2), "utf8");
    fs.renameSync(tmp, filePath);
  } catch (err) {
    console.error(`Error saving ${filePath}:`, err);
  }
}

function loadTasks() {
  return loadJson(TASKS_FILE, []);
}
function saveTasks(tasks) {
  saveJson(TASKS_FILE, tasks);
  broadcastTasksUpdate();
}

function loadUsers() {
  return loadJson(USERS_FILE, {});
}
function saveUsers(users) {
  saveJson(USERS_FILE, users);
}

function loadWipLimits() {
  return loadJson(WIP_LIMITS_FILE, DEFAULT_WIP_LIMITS);
}
function saveWipLimits(limits) {
  saveJson(WIP_LIMITS_FILE, limits);
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
      console.error("Mutation error:", err);
      reject(err);
    }
  }
  mutationProcessing = false;
}

/* -------------------- utilities -------------------- */

function genId() {
  return `${Date.now()}-${Math.floor(Math.random() * 100000)}`;
}

/* -------------------- recurrence helpers (effective state calculation) ---------- */

function parseDateSafe(v) {
  if (!v) return null;
  const d = new Date(v);
  return isNaN(d.getTime()) ? null : d;
}

function calcReadyAt(task) {
  const sd = parseDateSafe(task.scheduledDueAt);
  if (!sd) return null;
  const lead =
    Number(
      (task.recurrence && task.recurrence.leadTimeDays) ||
        task.leadTimeDays ||
        0,
    ) || 0;
  const readyMs = sd.getTime() - Math.round(lead * 24 * 60 * 60 * 1000);
  return new Date(readyMs).toISOString();
}

function anyDependencyUnresolved(tasks, task) {
  if (!Array.isArray(task.dependencies) || task.dependencies.length === 0)
    return false;
  const byId = new Map(tasks.map((t) => [t.id, t]));
  return task.dependencies.some((did) => {
    const dt = byId.get(did);
    return !dt || dt.state !== "Done";
  });
}

/**
 * computeEffectiveState(task, allTasks, now)
 * Returns object: { effectiveState, readyAt, scheduledDueAt, overdue }
 */
function computeEffectiveState(task, allTasks = [], now = new Date()) {
  const scheduledAt = parseDateSafe(task.scheduledDueAt);
  const readyAtIso = calcReadyAt(task);
  const readyAt = parseDateSafe(readyAtIso);

  // Debug logging for specific task
  if (task.id === "1766754296179-93047") {
    console.log(`\nDEBUG Task ${task.id}:`);
    console.log(`  scheduledAt: ${scheduledAt}`);
    console.log(`  readyAtIso: ${readyAtIso}`);
    console.log(`  readyAt: ${readyAt}`);
    console.log(`  now: ${now.toISOString()}`);
    console.log(`  stored state: ${task.state}`);
    console.log(
      `  now < readyAt: ${readyAt && now.getTime() < readyAt.getTime()}`,
    );
  }

  // Determine overdue:
  // - For recurring tasks with a numeric intervalDays, consider overdue only
  //   when now is past scheduledAt + (intervalDays / 2).
  // - For non-recurring tasks, keep original behavior (now >= scheduledAt).
  let overdue = false;
  if (scheduledAt) {
    overdue = false;
    if (task.recurrence && typeof task.recurrence === "object") {
      const intervalDays =
        Number(task.recurrence.intervalDays ?? task.recurrence.interval ?? 0) ||
        0;
      if (intervalDays > 0) {
        const halfMs = (intervalDays / 2) * 24 * 60 * 60 * 1000;
        overdue = now.getTime() >= scheduledAt.getTime() + halfMs;
      } else {
        // no interval -> fall back to strict due-time overdue
        overdue = now.getTime() >= scheduledAt.getTime();
      }
    } else {
      overdue = now.getTime() >= scheduledAt.getTime();
    }
  }

  if (task.state === "Done") {
    return {
      effectiveState: "Done",
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue: false,
    };
  }

  if (task.state === "Blocked") {
    return {
      effectiveState: "Blocked",
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue,
    };
  }
  if (task.state === "InProgress") {
    return {
      effectiveState: "InProgress",
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue,
    };
  }
  // Note: Don't return early for Suspended state - let timing logic override it

  if (task.recurrence && task.recurrence.paused) {
    return {
      effectiveState: "Suspended",
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue,
    };
  }

  // Check if task is not due yet - this should override dependency checks
  if (readyAt && now.getTime() < readyAt.getTime()) {
    return {
      effectiveState: "Waiting",
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue: false,
    };
  }

  // Only check dependencies if task is due/ready
  if (anyDependencyUnresolved(allTasks, task)) {
    return {
      effectiveState: "Suspended",
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue,
    };
  }

  if (!scheduledAt) {
    const fallback = task.state || "Ready";
    return {
      effectiveState: fallback,
      readyAt: readyAtIso,
      scheduledDueAt: null,
      overdue: false,
    };
  }

  // If stored state was Suspended, respect it after all other checks
  if (task.state === "Suspended") {
    return {
      effectiveState: "Suspended",
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue,
    };
  }

  return {
    effectiveState: "Ready",
    readyAt: readyAtIso,
    scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
    overdue,
  };
}

/* -------------------- user points -------------------- */

function awardPoints(userKey, points, reason = "") {
  if (!userKey) return null;
  const users = loadUsers();
  const key = String(userKey).trim();
  if (!key) return null;
  if (!users[key]) users[key] = { id: key, name: key, points: 0, history: [] };
  const u = users[key];
  const pts = Math.max(0, Math.round(points || 0));
  u.points = (u.points || 0) + pts;
  u.history = u.history || [];
  u.history.push({ ts: new Date().toISOString(), points: pts, reason });
  saveUsers(users);
  return u;
}

/* -------------------- WIP helpers -------------------- */

function wouldExceedWip(tasks, targetState, excludeTaskId = null) {
  const limits = WIP_LIMITS || loadWipLimits();
  const limit = limits[targetState];
  if (!Number.isFinite(limit)) return false;
  const count = tasks.filter(
    (t) => t.state === targetState && t.id !== excludeTaskId,
  ).length;
  return count + 1 > limit;
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

function computePriorities(tasks, nowIso = new Date(), config = {}) {
  const cfg = {
    MAX_WINDOW: config.MAX_WINDOW_days ?? 30,
    decay: config.decay ?? 0.5,
    w_u: config.w_u ?? 0.4,
    w_i: config.w_i ?? 0.6,
  };

  const now = nowIso instanceof Date ? nowIso : new Date(nowIso);

  /* ---------- dependency graph ---------- */

  const byId = new Map(tasks.map((t) => [t.id, t]));
  const dependents = new Map();
  for (const t of tasks) dependents.set(t.id, []);
  for (const t of tasks) {
    (t.dependencies || []).forEach((dep) => {
      if (dependents.has(dep)) dependents.get(dep).push(t.id);
    });
  }

  const inDegree = new Map();
  for (const t of tasks) inDegree.set(t.id, 0);
  for (const t of tasks) {
    for (const dep of t.dependencies || []) {
      if (!byId.has(dep)) {
        console.warn(`Task ${t.id} depends on missing task ${dep}`);
        continue;
      }
      inDegree.set(t.id, (inDegree.get(t.id) || 0) + 1);
    }
  }

  const q = [];
  for (const [id, deg] of inDegree.entries()) if (deg === 0) q.push(id);
  const topo = [];
  while (q.length) {
    const id = q.shift();
    topo.push(id);
    for (const kid of dependents.get(id) || []) {
      if (!inDegree.has(kid)) continue;
      inDegree.set(kid, inDegree.get(kid) - 1);
      if (inDegree.get(kid) === 0) q.push(kid);
    }
  }

  const topoSet = new Set(topo);
  const cycleNodes = tasks.filter((t) => !topoSet.has(t.id)).map((t) => t.id);
  const deadlockSet = new Set(cycleNodes);

  /* ---------- importance propagation ---------- */

  const rawR = new Map();
  for (const t of tasks) rawR.set(t.id, 0);

  for (let i = topo.length - 1; i >= 0; --i) {
    const id = topo[i];
    let sum = 0;
    for (const childId of dependents.get(id) || []) {
      const childRaw = rawR.get(childId) || 0;
      sum += 1 + cfg.decay * childRaw;
    }
    rawR.set(id, sum);
  }

  for (const id of cycleNodes) rawR.set(id, 0);

  const allRaw = Array.from(rawR.values()).sort((a, b) => a - b);

  function rawToPercentile(val) {
    // If there is only one value, or none, return 0 (no meaningful percentile)
    if (allRaw.length <= 1) return 0;

    // Count how many values are strictly less than val
    const lessCount = allRaw.filter((v) => v < val).length;

    // Use denom = allRaw.length - 1 so the max possible percentile is 100
    const denom = allRaw.length - 1;
    const pct = denom > 0 ? (lessCount / denom) * 100 : 0;

    return Math.max(0, Math.min(100, Math.round(pct)));
  }

  /* ---------- urgency calculation (UPDATED) ---------- */

  function computeUrgency(task) {
    // 1. Hard deadline always wins
    if (task.deadline) {
      const d = new Date(task.deadline);
      if (isNaN(d.getTime())) return 0;
      const msLeft = d.getTime() - now.getTime();
      const daysLeft = msLeft / (1000 * 60 * 60 * 24);
      if (daysLeft <= 0) return 100;
      if (daysLeft >= cfg.MAX_WINDOW) return 0;
      return Math.max(
        0,
        Math.min(100, Math.round(100 * (1 - daysLeft / cfg.MAX_WINDOW))),
      );
    }

    // 2. Rolling recurrence urgency grows as the due date approaches
    if (task.scheduledDueAt) {
      const d = new Date(task.scheduledDueAt);
      if (isNaN(d.getTime())) return 0;

      const isRolling =
        task.recurrence &&
        typeof task.recurrence === "object" &&
        task.recurrence.type === "rolling";

      const intervalDays =
        Number(
          (task.recurrence && task.recurrence.intervalDays) ??
            (task.recurrence && task.recurrence.interval) ??
            0,
        ) || 0;

      if (isRolling && intervalDays > 0) {
        // urgency should grow during the interval *leading up to* scheduledDueAt
        const msUntil = d.getTime() - now.getTime();
        const daysUntil = msUntil / (1000 * 60 * 60 * 24);

        if (daysUntil <= 0) return 100; // past due -> max urgency
        if (daysUntil >= intervalDays) return 0; // far away -> no urgency
        // scale from 0 -> 100 as we go from intervalDays -> 0
        return Math.max(
          0,
          Math.min(100, Math.round(100 * (1 - daysUntil / intervalDays))),
        );
      }

      // 3. Fallback: soft deadline behavior (non-rolling)
      const msLeft = d.getTime() - now.getTime();
      const daysLeft = msLeft / (1000 * 60 * 60 * 24);
      if (daysLeft <= 0) return 100;
      if (daysLeft >= cfg.MAX_WINDOW) return 0;
      return Math.max(
        0,
        Math.min(100, Math.round(100 * (1 - daysLeft / cfg.MAX_WINDOW))),
      );
    }

    return 0;
  }

  /* ---------- final priority ---------- */

  return tasks.map((t) => {
    const r = rawR.get(t.id) || 0;
    const I = rawToPercentile(r);
    const U = computeUrgency(t);
    const P = Math.max(
      1,
      Math.min(100, Math.round(cfg.w_u * U + cfg.w_i * I + 1)),
    );

    return Object.assign({}, t, {
      importanceRaw: r,
      importancePercentile: I,
      urgency: U,
      priority: P,
      deadlock: deadlockSet.has(t.id),
    });
  });
}

/* -------------------- recompute wrapper -------------------- */

function recomputeAllPriorities(tasks = null) {
  const cfg = { MAX_WINDOW_days: 30, decay: 0.5, w_u: 0.4, w_i: 0.6 };
  const ts = tasks ?? loadTasks();
  const updated = computePriorities(ts, new Date(), cfg);
  const changed = [];
  for (const u of updated) {
    const t = ts.find((x) => x.id === u.id);
    if (!t) continue;
    const before = {
      priority: t.priority,
      urgency: t.urgency,
      importancePercentile: t.importancePercentile,
    };
    t.priority = u.priority;
    t.urgency = u.urgency;
    t.importancePercentile = u.importancePercentile;
    t.importanceRaw = u.importanceRaw;
    t.deadlock = u.deadlock;
    if (
      before.priority !== t.priority ||
      before.urgency !== t.urgency ||
      before.importancePercentile !== t.importancePercentile
    ) {
      changed.push({
        id: t.id,
        before,
        after: {
          priority: t.priority,
          urgency: t.urgency,
          importancePercentile: t.importancePercentile,
        },
      });
    }
  }
  if (changed.length) console.log("Priorities/metrics changed:", changed);
  saveTasks(ts);
  return ts;
}

/* -------------------- Authentication API -------------------- */

app.post("/auth/register", async (req, res) => {
  try {
    const { username, password } = req.body;

    if (!username || !password) {
      return res.status(400).json({ error: "Username and password required" });
    }

    if (username.length < 3) {
      return res
        .status(400)
        .json({ error: "Username must be at least 3 characters" });
    }

    if (password.length < 6) {
      return res
        .status(400)
        .json({ error: "Password must be at least 6 characters" });
    }

    const users = loadUsers();

    if (users[username]) {
      return res.status(400).json({ error: "Username already exists" });
    }

    const hashedPassword = await hashPassword(password);
    users[username] = createUser(username, hashedPassword);
    saveUsers(users);

    req.session.userId = username;
    res.json({ success: true, username });
  } catch (err) {
    console.error("Register error:", err);
    res.status(500).json({ error: "Registration failed" });
  }
});

app.post("/auth/login", async (req, res) => {
  try {
    const { username, password } = req.body;

    if (!username || !password) {
      return res.status(400).json({ error: "Username and password required" });
    }

    const users = loadUsers();
    const user = users[username];

    if (!user) {
      return res.status(401).json({ error: "Invalid username or password" });
    }

    const valid = await verifyPassword(password, user.password);

    if (!valid) {
      return res.status(401).json({ error: "Invalid username or password" });
    }

    req.session.userId = username;
    res.json({ success: true, username });
  } catch (err) {
    console.error("Login error:", err);
    res.status(500).json({ error: "Login failed" });
  }
});

app.post("/auth/logout", (req, res) => {
  req.session.destroy((err) => {
    if (err) {
      console.error("Logout error:", err);
      return res.status(500).json({ error: "Logout failed" });
    }
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
      });
    } else {
      req.session.destroy();
      res.json({ authenticated: false });
    }
  } else {
    res.json({ authenticated: false });
  }
});

/* -------------------- API handlers -------------------- */

app.get("/", (req, res) => res.send("Server running."));

app.get("/tasks", (req, res) => {
  try {
    const tasks = loadTasks();
    const now = new Date();
    const enriched = tasks.map((t) => {
      const eff = computeEffectiveState(t, tasks, now);
      return Object.assign({}, t, {
        effectiveState: eff.effectiveState,
        readyAt: eff.readyAt || null,
        scheduledDueAt: eff.scheduledDueAt || null,
        overdue: !!eff.overdue,
      });
    });
    res.json(enriched);
  } catch (err) {
    console.error("GET /tasks error while enriching:", err);
    res.status(500).json({ error: "Internal error fetching tasks" });
  }
});

app.post("/tasks", requireAuth, async (req, res) => {
  try {
    const created = await enqueueMutation(async () => {
      const tasks = loadTasks();

      const deps = Array.isArray(req.body.dependencies)
        ? req.body.dependencies
        : [];
      const validDeps = deps.filter((d) => tasks.some((t) => t.id === d));
      if (deps.length && validDeps.length !== deps.length) {
        console.warn("POST /tasks: some dependencies were invalid and dropped");
      }

      const titleRaw = req.body.title || "Untitled Task";
      const title = String(titleRaw).trim().slice(0, 200);
      const description = (req.body.description || "").toString().trim();

      const newTask = {
        id: genId(),
        title: title || "Untitled Task",
        description: description || "",
        state: "Ready",
        deadline: req.body.deadline || undefined,
        dependencies: validDeps,
        picker: null,
        points_snapshot: undefined,
        picked_at: undefined,
        awarded: undefined,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        created_by: req.session.userId,
        meta: {},
      };

      // Only create recurrence when explicitly provided and not 'none'
      if (req.body.recurrence && typeof req.body.recurrence === "object") {
        if (req.body.recurrence.type && req.body.recurrence.type !== "none") {
          newTask.recurrence = {};
          const r = req.body.recurrence;
          if (["rolling", "anchored"].includes(r.type))
            newTask.recurrence.type = r.type;
          if (Number.isFinite(Number(r.intervalDays)))
            newTask.recurrence.intervalDays = Number(r.intervalDays);
          if (Array.isArray(r.weekdays))
            newTask.recurrence.weekdays = r.weekdays
              .map((n) => Number(n))
              .filter((x) => Number.isFinite(x));
          if (Number.isFinite(Number(r.leadTimeDays)))
            newTask.recurrence.leadTimeDays = Number(r.leadTimeDays);
          if ("paused" in r) newTask.recurrence.paused = !!r.paused;
        }
      }

      if (req.body.scheduledDueAt)
        newTask.scheduledDueAt = req.body.scheduledDueAt;
      if (req.body.lastCompletedAt)
        newTask.lastCompletedAt = req.body.lastCompletedAt;

      tasks.push(newTask);

      recomputeAllPriorities(tasks);
      saveTasks(tasks);

      // Return the updated task with computed priorities
      const updatedTask = tasks.find((t) => t.id === newTask.id);
      return updatedTask || newTask;
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
      if (typeof newState !== "string" || !newState) {
        throw {
          status: 400,
          body: { error: "Missing or invalid 'state' in body" },
        };
      }

      let newPickerRaw = "picker" in req.body ? req.body.picker : undefined;

      // Auto-set picker to logged-in user when claiming task (moving to InProgress)
      if (newState === "InProgress" && newPickerRaw === undefined) {
        newPickerRaw = req.session.userId;
      }

      const newPicker =
        typeof newPickerRaw === "string" ? newPickerRaw.trim() : newPickerRaw;
      const note = req.body.note || "";

      // actionability check with overdue exception
      const eff = computeEffectiveState(task, tasks, new Date());
      const scheduledAt = parseDateSafe(task.scheduledDueAt);
      const now = new Date();
      const isOverdue = scheduledAt
        ? now.getTime() >= scheduledAt.getTime()
        : false;

      if (newState === "InProgress") {
        if (eff.effectiveState !== "Ready" && !isOverdue) {
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
          eff.effectiveState !== "Ready" &&
          !isOverdue
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
        return task;
      }

      if (newState === "InProgress") {
        task.state = "InProgress";
        if (newPicker !== undefined && newPicker !== "") {
          task.picker = newPicker;
          task.picker_history = task.picker_history || [];
          task.picker_history.push({
            ts: new Date().toISOString(),
            picker: newPicker,
            action: "picked",
          });
        }
        if (typeof task.points_snapshot !== "number") {
          const updated = computePriorities(tasks, new Date(), {
            MAX_WINDOW_days: 30,
            decay: 0.5,
            w_u: 0.4,
            w_i: 0.6,
          });
          const u = updated.find((x) => x.id === task.id);
          const snap =
            u && typeof u.priority === "number"
              ? u.priority
              : task.priority || 0;
          task.points_snapshot = snap;
          task.points_snapshot_created_at = new Date().toISOString();
          task.points_snapshot_created_by = task.picker || newPicker || null;
          task.picked_at = new Date().toISOString();
          task.points_history = task.points_history || [];
          task.points_history.push({
            ts: task.points_snapshot_created_at,
            snapshot: snap,
            by: task.points_snapshot_created_by,
          });
        }
        task.updated_at = new Date().toISOString();
        return persistAndReturn();
      }

      if (newState === "Blocked") {
        if (wouldExceedWip(tasks, "Blocked", task.id)) {
          throw {
            status: 400,
            body: {
              error: `WIP limit exceeded for Blocked. Limit: ${WIP_LIMITS["Blocked"]}`,
            },
          };
        }
        task.state = "Blocked";
        if (newPicker !== undefined && newPicker !== "") {
          task.picker = newPicker;
          task.picker_history = task.picker_history || [];
          task.picker_history.push({
            ts: new Date().toISOString(),
            picker: newPicker,
            action: "blocked",
          });
        }
        task.meta = task.meta || {};
        task.meta.block_note = note;
        task.updated_at = new Date().toISOString();
        return persistAndReturn();
      }

      if (newState === "Done") {
        const depsNotDone = (task.dependencies || []).some((depId) => {
          const depTask = tasks.find((t) => t.id === depId);
          return !depTask || depTask.state !== "Done";
        });
        if (depsNotDone) {
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

        const completedAtIso = new Date().toISOString();
        task.lastCompletedAt = completedAtIso;
        task.state = "Done";
        task.updated_at = completedAtIso;

        tasks.forEach((t) => {
          if (
            t.state === "Suspended" &&
            (t.dependencies || []).includes(task.id)
          ) {
            const allDepsDone = (t.dependencies || []).every((depId) => {
              const depTask = tasks.find((x) => x.id === depId);
              return depTask && depTask.state === "Done";
            });
            if (allDepsDone) {
              t.state = "Ready";
              t.updated_at = new Date().toISOString();
            }
          }
        });

        if (!wasBlocked && pickerKey && pointsToAward > 0) {
          const user = awardPoints(
            pickerKey,
            pointsToAward,
            `Completed task ${task.id} (${task.title})`,
          );
          task.awarded = {
            to: pickerKey,
            points: pointsToAward,
            ts: new Date().toISOString(),
          };
          task.points_snapshot_awarded = true;
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

        recomputeAllPriorities(tasks);
        saveTasks(tasks);
        return task;
      }

      task.state = newState;
      if (newPicker !== undefined) {
        if (newPicker === null || newPicker === "") {
          // Unclaim the task
          task.picker = null;
        } else {
          task.picker = newPicker;
          task.picker_history = task.picker_history || [];
          task.picker_history.push({
            ts: new Date().toISOString(),
            picker: newPicker,
            action: "state-change",
          });
        }
      }
      task.updated_at = new Date().toISOString();

      recomputeAllPriorities(tasks);
      saveTasks(tasks);
      return task;
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

/* block/suspend/dependencies/remedy/delete handlers unchanged (keep existing semantics) */

app.patch("/tasks/:id/block", requireAuth, async (req, res) => {
  try {
    const updated = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const task = tasks.find((t) => t.id === req.params.id);
      if (!task) throw { status: 404, body: { error: "Task not found" } };

      if (wouldExceedWip(tasks, "Blocked", task.id)) {
        throw {
          status: 400,
          body: {
            error: `WIP limit exceeded for Blocked. Limit: ${WIP_LIMITS["Blocked"]}`,
          },
        };
      }
      task.state = "Blocked";
      task.meta = task.meta || {};
      task.meta.block_note = req.body.note || "";
      task.updated_at = new Date().toISOString();
      recomputeAllPriorities(tasks);
      saveTasks(tasks);
      return task;
    });
    res.json(updated);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("PATCH /tasks/:id/block error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

app.patch("/tasks/:id/suspend", requireAuth, async (req, res) => {
  try {
    const updated = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const task = tasks.find((t) => t.id === req.params.id);
      if (!task) throw { status: 404, body: { error: "Task not found" } };

      if (wouldExceedWip(tasks, "Suspended", task.id)) {
        throw {
          status: 400,
          body: {
            error: `WIP limit exceeded for Suspended. Limit: ${WIP_LIMITS["Suspended"]}`,
          },
        };
      }
      task.state = "Suspended";
      task.updated_at = new Date().toISOString();
      recomputeAllPriorities(tasks);
      saveTasks(tasks);
      return task;
    });
    res.json(updated);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("PATCH /tasks/:id/suspend error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

app.post("/tasks/:id/dependencies", requireAuth, async (req, res) => {
  try {
    const updated = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const task = tasks.find((t) => t.id === req.params.id);
      if (!task) throw { status: 404, body: { error: "Task not found" } };

      const depId = req.body.dependencyId;
      if (!depId || !tasks.find((t) => t.id === depId)) {
        throw { status: 400, body: { error: "Dependency task not found" } };
      }
      if (depId === task.id)
        throw { status: 400, body: { error: "Task cannot depend on itself" } };

      if (wouldCreateCycle(tasks, task.id, depId)) {
        throw {
          status: 400,
          body: { error: "Adding this dependency would create a cycle" },
        };
      }

      if (!task.dependencies) task.dependencies = [];
      if (!task.dependencies.includes(depId)) {
        task.dependencies.push(depId);
        task.updated_at = new Date().toISOString();

        const hasUnresolvedDeps = (task.dependencies || []).some((did) => {
          const depTask = tasks.find((x) => x.id === did);
          return !depTask || depTask.state !== "Done";
        });

        if (hasUnresolvedDeps && task.state !== "Done") {
          task.state = "Suspended";
          task.updated_at = new Date().toISOString();
        }

        recomputeAllPriorities(tasks);
        saveTasks(tasks);
      }

      return task;
    });
    res.json(updated);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("POST /tasks/:id/dependencies error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

app.post("/tasks/:id/remedy", async (req, res) => {
  try {
    const result = await enqueueMutation(async () => {
      const tasks = loadTasks();
      const blockedTask = tasks.find((t) => t.id === req.params.id);
      if (!blockedTask)
        throw { status: 404, body: { error: "Blocked task not found" } };

      const deadline =
        req.body.deadline !== undefined && req.body.deadline !== null
          ? req.body.deadline
          : blockedTask.deadline;
      const description =
        req.body.description && String(req.body.description).trim() !== ""
          ? req.body.description
          : `Remedy for: ${blockedTask.title}`;

      const titleRaw = req.body.title || `Remedy for ${blockedTask.title}`;
      const title = String(titleRaw).trim().slice(0, 200);

      const newTask = {
        id: genId(),
        title: title || `Remedy for ${blockedTask.title}`,
        description: description || `Created to unblock: ${blockedTask.title}`,
        state: "Ready",
        deadline: deadline || undefined,
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
      blockedTask.updated_at = new Date().toISOString();

      recomputeAllPriorities(tasks);
      saveTasks(tasks);

      return { blockedTask, remedyTask: newTask };
    });

    res.json(result);
  } catch (err) {
    if (err && err.status && err.body)
      return res.status(err.status).json(err.body);
    console.error("POST /tasks/:id/remedy error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

app.get("/tasks/active", (req, res) => {
  const tasks = loadTasks();
  const updated = computePriorities(tasks, new Date(), {
    MAX_WINDOW_days: 30,
    decay: 0.5,
    w_u: 0.4,
    w_i: 0.6,
  });
  const activeTasks = updated
    .filter((t) => ["Ready", "InProgress"].includes(t.state))
    .sort((a, b) => b.priority - a.priority);
  res.json(activeTasks);
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

      // Build lookup
      const byId = new Map(tasks.map((t) => [t.id, t]));

      // 1) Build the dependency closure (outgoing edges) from targetId.
      // This is: target and every task that target depends on (recursively).
      const closure = new Set();
      const stack = [targetId];
      while (stack.length) {
        const id = stack.pop();
        if (closure.has(id)) continue;
        closure.add(id);
        const t = byId.get(id);
        if (!t || !Array.isArray(t.dependencies)) continue;
        for (const depId of t.dependencies) {
          if (!closure.has(depId)) stack.push(depId);
        }
      }

      // 2) Find tasks outside closure that depend on the target (incoming edges).
      const tasksOutsideClosure = tasks.filter((t) => !closure.has(t.id));
      // If any outside task depends on targetId, we should warn and require confirm.
      const incomingDependents = tasksOutsideClosure
        .filter(
          (t) =>
            Array.isArray(t.dependencies) && t.dependencies.includes(targetId),
        )
        .map((t) => ({ id: t.id, title: t.title, state: t.state }));

      if (incomingDependents.length && !confirm) {
        // 409 Conflict + list of dependents so the frontend can prompt the user.
        throw {
          status: 409,
          body: {
            error:
              "Other tasks depend on this task. Confirm deletion to proceed; this will affect the dependent tasks.",
            dependents: incomingDependents,
          },
        };
      }

      // 3) Determine the actual set to delete.
      // Rules:
      //  - always delete the explicit target
      //  - for other nodes in closure: delete them only if NO task outside the closure depends on them
      const tasksOutside = tasks.filter((t) => !closure.has(t.id));
      const hasExternalDep = (nodeId) =>
        tasksOutside.some(
          (t) =>
            Array.isArray(t.dependencies) && t.dependencies.includes(nodeId),
        );

      const toDelete = new Set();
      toDelete.add(targetId);
      for (const id of closure) {
        if (id === targetId) continue;
        if (!hasExternalDep(id)) {
          toDelete.add(id);
        }
      }

      // 4) Remove toDelete from the tasks list
      const beforeCount = tasks.length;
      tasks = tasks.filter((t) => !toDelete.has(t.id));
      const deleted = Array.from(toDelete);

      // 5) Clean remaining tasks: strip deleted ids from dependencies and remedy_for.
      //    If a task had state 'suspended' and we removed dependencies that it was waiting on,
      //    move it back to 'blocked' (per your requirement).
      tasks.forEach((t) => {
        let changed = false;
        if (Array.isArray(t.dependencies)) {
          const filtered = t.dependencies.filter((d) => !toDelete.has(d));
          if (filtered.length !== t.dependencies.length) {
            t.dependencies = filtered;
            changed = true;
          }
        }
        if (t.remedy_for && toDelete.has(t.remedy_for)) {
          delete t.remedy_for;
          changed = true;
        }

        if (changed) {
          // If the task used to be suspended, move it to blocked.
          // We rely on tasks having a `state` field (suspended/blocked/etc).
          // If your code stores state differently, adapt this bit accordingly.
          if (t.state === "suspended") {
            t.state = "blocked";
            // Optionally mark when it was moved back to blocked:
            t.blocked_at = new Date().toISOString();
          }
          t.updated_at = new Date().toISOString();
        }
      });

      // 6) Recompute derived values and persist.
      recomputeAllPriorities(tasks);
      saveTasks(tasks);

      return {
        message: "Deleted",
        deleted,
        removed_count: beforeCount - tasks.length,
        adjusted: tasks
          .filter((t) => t.state === "blocked")
          .map((t) => ({ id: t.id, title: t.title, state: t.state })),
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

      if (typeof req.body.title === "string") {
        task.title = req.body.title.trim().slice(0, 200) || task.title;
      }
      if ("description" in req.body) {
        task.description = (req.body.description || "").toString().trim();
      }
      if ("scheduledDueAt" in req.body) {
        task.scheduledDueAt = req.body.scheduledDueAt || null;
      }

      let recurrenceEdited = false;
      if (req.body.recurrence && typeof req.body.recurrence === "object") {
        recurrenceEdited = true;
        // special-case: user explicitly selected 'none' to remove recurrence
        if (req.body.recurrence.type === "none") {
          delete task.recurrence;
        } else {
          task.recurrence = task.recurrence || {};
          if ("leadTimeDays" in req.body.recurrence) {
            const v = Number(req.body.recurrence.leadTimeDays);
            task.recurrence.leadTimeDays = Number.isFinite(v)
              ? v
              : task.recurrence.leadTimeDays;
          }
          if ("paused" in req.body.recurrence) {
            task.recurrence.paused = !!req.body.recurrence.paused;
          }
          if (typeof req.body.recurrence.type === "string") {
            const tval = req.body.recurrence.type;
            if (["rolling", "anchored"].includes(tval))
              task.recurrence.type = tval;
          }
          if ("intervalDays" in req.body.recurrence) {
            const iv = parseInt(req.body.recurrence.intervalDays, 10);
            if (Number.isFinite(iv) && iv > 0)
              task.recurrence.intervalDays = iv;
          }
          if (Array.isArray(req.body.recurrence.weekdays)) {
            task.recurrence.weekdays = req.body.recurrence.weekdays
              .slice(0, 7)
              .map((n) => Number(n))
              .filter((x) => Number.isFinite(x));
          }
        }
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
      if (Array.isArray(req.body.dependencies)) {
        const newDeps = req.body.dependencies.filter((id) => {
          return typeof id === "string" && id.trim() !== "";
        });

        // Validate each dependency exists and check for cycles
        for (const depId of newDeps) {
          if (depId === task.id) {
            throw {
              status: 400,
              body: { error: "Task cannot depend on itself" },
            };
          }

          const depTask = tasks.find((t) => t.id === depId);
          if (!depTask) {
            throw {
              status: 400,
              body: { error: `Dependency task ${depId} not found` },
            };
          }

          // Check if adding this dependency would create a cycle
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

        // If task has unresolved dependencies and isn't Done, suspend it
        const hasUnresolvedDeps = newDeps.some((did) => {
          const depTask = tasks.find((x) => x.id === did);
          return !depTask || depTask.state !== "Done";
        });

        if (hasUnresolvedDeps && task.state !== "Done") {
          task.state = "Suspended";
        }
      }

      task.updated_at = new Date().toISOString();

      recomputeAllPriorities(tasks);
      saveTasks(tasks);
      return task;
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

app.get("/wip-limits", (req, res) => {
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

      // Update only provided limits
      if (typeof req.body.Ready === "number" || req.body.Ready === null) {
        limits.Ready = req.body.Ready;
      }
      if (
        typeof req.body.InProgress === "number" ||
        req.body.InProgress === null
      ) {
        limits.InProgress = req.body.InProgress;
      }
      if (typeof req.body.Blocked === "number" || req.body.Blocked === null) {
        limits.Blocked = req.body.Blocked;
      }
      if (
        typeof req.body.Suspended === "number" ||
        req.body.Suspended === null
      ) {
        limits.Suspended = req.body.Suspended;
      }
      if (typeof req.body.Waiting === "number" || req.body.Waiting === null) {
        limits.Waiting = req.body.Waiting;
      }
      if (typeof req.body.Done === "number" || req.body.Done === null) {
        limits.Done = req.body.Done;
      }

      saveWipLimits(limits);
      WIP_LIMITS = limits; // Update cached limits
      return limits;
    });

    res.json(updated);
  } catch (err) {
    console.error("PATCH /wip-limits error:", err);
    res.status(500).json({ error: "Internal error" });
  }
});

/* -------------------- server & periodic recompute -------------------- */

const PORT = process.env.PORT || 3000;
const server = require("http").createServer(app);

// WebSocket setup
const wss = new WebSocketServer({ server });
const clients = new Set();

wss.on("connection", (ws) => {
  console.log("New WebSocket client connected");
  clients.add(ws);

  // Send initial tasks on connection
  try {
    const tasks = loadTasks();
    const now = new Date();
    const enriched = tasks.map((t) => {
      const eff = computeEffectiveState(t, tasks, now);
      return Object.assign({}, t, {
        effectiveState: eff.effectiveState,
        readyAt: eff.readyAt || null,
        scheduledDueAt: eff.scheduledDueAt || null,
        overdue: !!eff.overdue,
      });
    });
    ws.send(JSON.stringify({ type: "tasks", data: enriched }));
  } catch (err) {
    console.error("Error sending initial tasks:", err);
  }

  ws.on("close", () => {
    console.log("WebSocket client disconnected");
    clients.delete(ws);
  });

  ws.on("error", (err) => {
    console.error("WebSocket error:", err);
    clients.delete(ws);
  });
});

// Broadcast tasks update to all connected clients
function broadcastTasksUpdate() {
  if (clients.size === 0) return;

  try {
    const tasks = loadTasks();
    const now = new Date();
    const enriched = tasks.map((t) => {
      const eff = computeEffectiveState(t, tasks, now);
      return Object.assign({}, t, {
        effectiveState: eff.effectiveState,
        readyAt: eff.readyAt || null,
        scheduledDueAt: eff.scheduledDueAt || null,
        overdue: !!eff.overdue,
      });
    });

    const message = JSON.stringify({ type: "tasks", data: enriched });
    clients.forEach((client) => {
      if (client.readyState === 1) {
        // 1 = OPEN
        client.send(message);
      }
    });
  } catch (err) {
    console.error("Error broadcasting tasks:", err);
  }
}

//server.listen(PORT, () => {
server.listen(PORT, "0.0.0.0", () => {
  WIP_LIMITS = loadWipLimits();
  console.log(`Server listening on port ${PORT}`);
  console.log("WIP limits loaded:", WIP_LIMITS);
});

setInterval(
  () => {
    enqueueMutation(async () => {
      try {
        recomputeAllPriorities();
        console.log("Periodic recompute done:", new Date().toISOString());
      } catch (err) {
        console.error("Periodic recompute error:", err);
      }
    }).catch((err) => console.error("Periodic recompute enqueue failed:", err));
  },
  10 * 60 * 1000,
);
