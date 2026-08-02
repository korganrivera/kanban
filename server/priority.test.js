const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const testDataDir = fs.mkdtempSync(path.join(os.tmpdir(), "kanban-priority-test-"));
process.env.KANBAN_DATA_DIR = testDataDir;
process.env.SESSION_SECRET = "priority-test-session-secret-at-least-32-characters";
test.after(() => fs.rmSync(testDataDir, { recursive: true, force: true }));

const {
  buildPriorityContext,
  computeRawImportance,
  computeImportanceScore,
  computeUrgency,
  computeEffectiveState,
  computePriority,
  computePriorities,
  wouldExceedWip,
  clearClaimUnlessClaimRetained,
  reverseCompletionAward,
  restoreTaskCompletion,
} = require("./index.js");

function task(id, overrides = {}) {
  return {
    id,
    title: id,
    state: "Ready",
    dependencies: [],
    ...overrides,
  };
}

const NOW = new Date("2026-04-15T12:00:00.000Z");

test("no dependents and no due date stays at baseline priority", () => {
  const tasks = [task("A")];
  const context = buildPriorityContext(tasks);
  const result = computePriority(tasks[0], context, NOW);

  assert.equal(computeRawImportance(tasks[0], context), 0);
  assert.equal(computeImportanceScore(0, 0.5), 0);
  assert.equal(result.urgency, 0);
  assert.equal(result.priority, 1);
});

test("one direct dependent increases importance and priority", () => {
  const tasks = [task("A"), task("B", { dependencies: ["A"] })];
  const context = buildPriorityContext(tasks);
  const result = computePriority(tasks[0], context, NOW);

  assert.equal(result.rawImportance, 1);
  assert.ok(result.importance > 0);
  assert.equal(result.urgency, 0);
  assert.ok(result.priority > 1);
});

test("dependency chain applies half-strength decay per downstream hop", () => {
  const tasks = [
    task("A"),
    task("B", { dependencies: ["A"] }),
    task("C", { dependencies: ["B"] }),
  ];
  const context = buildPriorityContext(tasks);

  const rawA = computeRawImportance(tasks[0], context);
  const rawB = computeRawImportance(tasks[1], context);
  const rawC = computeRawImportance(tasks[2], context);
  const scored = computePriorities(tasks, NOW);

  assert.equal(rawC, 0);
  assert.equal(rawB, 1);
  assert.equal(rawA, 1.5);
  assert.ok(scored.find((t) => t.id === "A").priority > scored.find((t) => t.id === "B").priority);
  assert.ok(scored.find((t) => t.id === "B").priority > scored.find((t) => t.id === "C").priority);
});

test("far future due date keeps urgency near zero", () => {
  const urgency = computeUrgency(30, 0.8, 3);
  assert.ok(urgency >= 0);
  assert.ok(urgency < 0.001);
});

test("three days until due is about half the urgency ceiling", () => {
  const urgency = computeUrgency(3, 0.8, 3);
  assert.equal(urgency, 24.75);
});

test("task due today is urgent but stays below the urgency ceiling", () => {
  const urgency = computeUrgency(0, 0.8, 3);
  assert.ok(urgency > 40);
  assert.ok(urgency < 49.5);
});

test("overdue task outranks due-today urgency without exceeding the bound", () => {
  const dueToday = computeUrgency(0, 0.8, 3);
  const overdue = computeUrgency(-1, 0.8, 3);
  assert.ok(overdue > dueToday);
  assert.ok(overdue < 49.5);
});

test("very overdue urgency approaches the ceiling without exceeding it", () => {
  const urgency = computeUrgency(-30, 0.8, 3);
  assert.ok(urgency > 49.49);
  assert.ok(urgency < 49.5);
});

test("high importance and high urgency combine near the top end but stay below 100", () => {
  const tasks = [
    task("root", { scheduledDueAt: "2026-04-14T12:00:00.000Z" }),
    task("d1", { dependencies: ["root"] }),
    task("d2", { dependencies: ["root"] }),
    task("d3", { dependencies: ["root"] }),
    task("d4", { dependencies: ["root"] }),
    task("d5", { dependencies: ["root"] }),
    task("d6", { dependencies: ["root"] }),
  ];
  const context = buildPriorityContext(tasks);
  const result = computePriority(tasks[0], context, NOW);

  assert.ok(result.importance > 45);
  assert.ok(result.urgency > 45);
  assert.ok(result.priority >= 90);
  assert.ok(result.priority < 100);
});

test("dependency cycles do not recurse forever and fall back safely", () => {
  const tasks = [
    task("A", { dependencies: ["B"] }),
    task("B", { dependencies: ["A"] }),
  ];
  const context = buildPriorityContext(tasks);
  const results = computePriorities(tasks, NOW);

  assert.equal(computeRawImportance(tasks[0], context), 0);
  assert.equal(computeRawImportance(tasks[1], context), 0);
  assert.equal(results[0].deadlock, true);
  assert.equal(results[1].deadlock, true);
  assert.equal(results[0].priority, 1);
  assert.equal(results[1].priority, 1);
});

test("in-progress task rescheduled into the future waits until ready", () => {
  const future = task("future", {
    state: "InProgress",
    picker: "korgan",
    scheduledDueAt: "2026-04-20T12:00:00.000Z",
    leadTimeDays: 0,
  });

  const beforeDue = computeEffectiveState(future, [future], NOW);
  assert.equal(beforeDue.effectiveState, "Waiting");

  const atDue = computeEffectiveState(
    future,
    [future],
    new Date("2026-04-20T12:00:00.000Z"),
  );
  assert.equal(atDue.effectiveState, "InProgress");
});

test("unclaimed in-progress task becomes ready when its future gate opens", () => {
  const future = task("future", {
    state: "InProgress",
    picker: null,
    scheduledDueAt: "2026-04-20T12:00:00.000Z",
    leadTimeDays: 0,
  });

  const beforeDue = computeEffectiveState(future, [future], NOW);
  assert.equal(beforeDue.effectiveState, "Waiting");

  const atDue = computeEffectiveState(
    future,
    [future],
    new Date("2026-04-20T12:00:00.000Z"),
  );
  assert.equal(atDue.effectiveState, "Ready");
});

test("wip limits count effective column state instead of stored state", () => {
  const tasks = [
    task("visible-1", { state: "InProgress", picker: "korgan" }),
    task("visible-2", { state: "InProgress", picker: "korgan" }),
    task("visible-3", { state: "InProgress", picker: "korgan" }),
    task("waiting", {
      state: "InProgress",
      picker: "korgan",
      scheduledDueAt: "2026-04-20T12:00:00.000Z",
      leadTimeDays: 0,
    }),
    task("candidate", { state: "Ready" }),
  ];

  assert.equal(
    wouldExceedWip(
      tasks,
      "InProgress",
      "candidate",
      NOW,
      { InProgress: 4 },
    ),
    false,
  );
});

test("claimed task is declaimed when it is no longer effectively in progress", () => {
  const future = task("future", {
    state: "InProgress",
    picker: "korgan",
    picked_at: "2026-04-15T12:00:00.000Z",
    scheduledDueAt: "2026-04-20T12:00:00.000Z",
    leadTimeDays: 0,
  });
  const tasks = [future];

  assert.equal(clearClaimUnlessClaimRetained(future, tasks, NOW), true);
  assert.equal(future.picker, null);
  assert.equal(future.picked_at, undefined);
  assert.equal(future.unclaimed_at, NOW.toISOString());
  assert.equal(future.state, "Ready");
});

test("claimed task remains claimed while effectively in progress", () => {
  const active = task("active", {
    state: "InProgress",
    picker: "korgan",
    picked_at: "2026-04-15T12:00:00.000Z",
  });
  const tasks = [active];

  assert.equal(clearClaimUnlessClaimRetained(active, tasks, NOW), false);
  assert.equal(active.picker, "korgan");
  assert.equal(active.picked_at, "2026-04-15T12:00:00.000Z");
});

test("done task keeps claim metadata for completion attribution", () => {
  const done = task("done", {
    state: "Done",
    picker: "korgan",
    picked_at: "2026-04-15T12:00:00.000Z",
  });
  const tasks = [done];

  assert.equal(clearClaimUnlessClaimRetained(done, tasks, NOW), false);
  assert.equal(done.picker, "korgan");
  assert.equal(done.picked_at, "2026-04-15T12:00:00.000Z");
});

test("completion undo restores a recurring task and released dependent", () => {
  const completedAt = "2026-04-15T13:00:00.000Z";
  const original = task("recurring", {
    state: "InProgress",
    picker: "korgan",
    picked_at: "2026-04-15T12:00:00.000Z",
    scheduledDueAt: "2026-04-15T12:30:00.000Z",
    recurrence: { type: "rolling", intervalDays: 7 },
    updated_at: "2026-04-15T12:00:00.000Z",
  });
  const completed = task("recurring", {
    state: "Ready",
    picker: null,
    scheduledDueAt: "2026-04-22T13:00:00.000Z",
    recurrence: { type: "rolling", intervalDays: 7 },
    updated_at: completedAt,
    completionUndo: {
      completedAt,
      previousTask: original,
      releasedDependents: [
        {
          id: "dependent",
          previousState: "Suspended",
          previousUpdatedAt: "2026-04-14T09:00:00.000Z",
          releasedAt: completedAt,
        },
      ],
    },
  });
  const dependent = task("dependent", {
    state: "Ready",
    dependencies: ["recurring"],
    updated_at: completedAt,
  });
  const tasks = [completed, dependent];

  restoreTaskCompletion(
    completed,
    tasks,
    "2026-04-15T13:01:00.000Z",
  );

  assert.equal(completed.state, "InProgress");
  assert.equal(completed.picker, "korgan");
  assert.equal(completed.scheduledDueAt, "2026-04-15T12:30:00.000Z");
  assert.equal(completed.completionUndo, undefined);
  assert.equal(completed.updated_at, "2026-04-15T13:01:00.000Z");
  assert.equal(dependent.state, "Suspended");
  assert.equal(dependent.updated_at, "2026-04-14T09:00:00.000Z");
});

test("completion undo reverses only its exact points history entry", () => {
  const users = {
    korgan: {
      points: 90,
      history: [
        { ts: "old", points: 40, reason: "Older completion" },
        {
          ts: "new",
          points: 50,
          reason: "Completed task task-1 (Task 1)",
          completionId: "task-1:new",
        },
      ],
    },
  };

  const result = reverseCompletionAward(users, {
    user: "korgan",
    completionId: "task-1:new",
  });

  assert.deepEqual(result, { reversed: true, points: 50 });
  assert.equal(users.korgan.points, 40);
  assert.deepEqual(users.korgan.history, [
    { ts: "old", points: 40, reason: "Older completion" },
  ]);
});

test("a completed occurrence satisfies a dependency on a recurring task", () => {
  const recurring = task("recurring", {
    state: "Ready",
    recurrence: { type: "rolling", intervalDays: 7 },
    lastCompletedAt: "2026-04-15T12:00:00.000Z",
    scheduledDueAt: "2026-04-22T12:00:00.000Z",
    created_at: "2026-04-01T12:00:00.000Z",
  });
  const dependent = task("dependent", {
    dependencies: ["recurring"],
    created_at: "2026-04-10T12:00:00.000Z",
  });

  assert.equal(
    computeEffectiveState(dependent, [recurring, dependent], NOW).effectiveState,
    "Ready",
  );
});

test("done dependents no longer inflate an active task's importance", () => {
  const prerequisite = task("prerequisite");
  const completedDependent = task("done", {
    state: "Done",
    dependencies: ["prerequisite"],
  });
  const context = buildPriorityContext([prerequisite, completedDependent]);

  assert.equal(computeRawImportance(prerequisite, context), 0);
});
