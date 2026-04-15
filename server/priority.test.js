const test = require("node:test");
const assert = require("node:assert/strict");

const {
  buildPriorityContext,
  computeRawImportance,
  computeImportanceScore,
  computeUrgency,
  computePriority,
  computePriorities,
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
