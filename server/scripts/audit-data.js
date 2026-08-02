#!/usr/bin/env node
"use strict";

const fs = require("fs");
const path = require("path");
const {
  TASK_STATES,
  computeEffectiveState,
} = require("../static/task-state.js");

const dataDir = path.resolve(
  process.env.KANBAN_DATA_DIR || path.join(__dirname, "..", "data"),
);
const tasks = JSON.parse(fs.readFileSync(path.join(dataDir, "tasks.json"), "utf8"));
const users = JSON.parse(fs.readFileSync(path.join(dataDir, "users.json"), "utf8"));
const limits = JSON.parse(
  fs.readFileSync(path.join(dataDir, "wip_limits.json"), "utf8"),
);
const errors = [];

if (!Array.isArray(tasks)) errors.push("tasks.json is not an array");
if (!users || Array.isArray(users) || typeof users !== "object") {
  errors.push("users.json is not an object");
}

const ids = new Set();
for (const task of Array.isArray(tasks) ? tasks : []) {
  if (!task || typeof task.id !== "string" || !task.id) {
    errors.push("A task has no valid ID");
    continue;
  }
  if (ids.has(task.id)) errors.push(`Duplicate task ID: ${task.id}`);
  ids.add(task.id);
  if (typeof task.title !== "string" || !task.title.trim()) {
    errors.push(`Task ${task.id} has no title`);
  }
  if (!TASK_STATES.includes(task.state)) {
    errors.push(`Task ${task.id} has invalid state ${task.state}`);
  }
  if (!Array.isArray(task.dependencies)) {
    errors.push(`Task ${task.id} dependencies are not an array`);
  } else if (new Set(task.dependencies).size !== task.dependencies.length) {
    errors.push(`Task ${task.id} has duplicate dependencies`);
  }
  const effectiveState = computeEffectiveState(task, tasks).effectiveState;
  if (task.picker && !["InProgress", "Done"].includes(effectiveState)) {
    errors.push(`Task ${task.id} has a claim in effective state ${effectiveState}`);
  }
  if (task.scheduledDueAt && Number.isNaN(new Date(task.scheduledDueAt).getTime())) {
    errors.push(`Task ${task.id} has an invalid scheduled date`);
  }
}

const tasksById = new Map(
  (Array.isArray(tasks) ? tasks : []).map((task) => [task.id, task]),
);
const visited = new Set();
const activePath = new Set();
const cycleNodes = new Set();

function visitDependencies(taskId) {
  if (activePath.has(taskId)) {
    cycleNodes.add(taskId);
    return;
  }
  if (visited.has(taskId)) return;
  activePath.add(taskId);
  for (const dependencyId of tasksById.get(taskId)?.dependencies || []) {
    if (activePath.has(dependencyId)) {
      cycleNodes.add(taskId);
      cycleNodes.add(dependencyId);
    } else if (tasksById.has(dependencyId)) {
      visitDependencies(dependencyId);
    }
  }
  activePath.delete(taskId);
  visited.add(taskId);
}

for (const taskId of tasksById.keys()) visitDependencies(taskId);
if (cycleNodes.size) {
  errors.push(`Dependency cycle detected near: ${[...cycleNodes].join(", ")}`);
}

for (const task of Array.isArray(tasks) ? tasks : []) {
  for (const dependencyId of task.dependencies || []) {
    if (!ids.has(dependencyId)) {
      errors.push(`Task ${task.id} has missing dependency ${dependencyId}`);
    }
    if (dependencyId === task.id) {
      errors.push(`Task ${task.id} depends on itself`);
    }
  }
}

for (const state of TASK_STATES) {
  const value = limits?.[state];
  if (value !== null && (!Number.isInteger(value) || value < 0)) {
    errors.push(`${state} has an invalid WIP limit`);
  }
}

if (errors.length) {
  for (const error of errors) console.error(error);
  process.exit(1);
}

console.log(
  `Data audit passed: ${tasks.length} tasks, ${Object.keys(users).length} user records.`,
);
