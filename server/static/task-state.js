(function exposeTaskState(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  } else {
    root.KanbanTaskState = api;
  }
})(typeof globalThis !== "undefined" ? globalThis : this, function taskStateFactory() {
  "use strict";

  const TASK_STATES = Object.freeze([
    "Waiting",
    "Ready",
    "InProgress",
    "Blocked",
    "Suspended",
    "Done",
  ]);

  function parseDateSafe(value) {
    if (!value) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
  }

  function calcReadyAt(task) {
    const scheduledAt = parseDateSafe(task && task.scheduledDueAt);
    if (!scheduledAt) return null;
    const recurrenceLead = Number(task?.recurrence?.leadTimeDays);
    const taskLead = Number(task?.leadTimeDays);
    const leadDays = Number.isFinite(recurrenceLead)
      ? recurrenceLead
      : Number.isFinite(taskLead)
        ? taskLead
        : 0;
    return new Date(
      scheduledAt.getTime() - Math.round(leadDays * 24 * 60 * 60 * 1000),
    ).toISOString();
  }

  function anyDependencyUnresolved(tasks, task) {
    if (!Array.isArray(task?.dependencies) || task.dependencies.length === 0) {
      return false;
    }
    const byId = new Map((tasks || []).map((candidate) => [candidate.id, candidate]));
    return task.dependencies.some((dependencyId) => {
      const dependency = byId.get(dependencyId);
      if (!dependency) return true;
      if (dependency.state === "Done") return false;
      const dependencyCompletedAt = parseDateSafe(dependency.lastCompletedAt);
      const requiredAfter = parseDateSafe(task.lastCompletedAt || task.created_at);
      return !(
        dependency.recurrence &&
        dependencyCompletedAt &&
        requiredAfter &&
        dependencyCompletedAt.getTime() >= requiredAfter.getTime()
      );
    });
  }

  function hasActiveClaim(task) {
    const picker =
      typeof task?.picker === "string" ? task.picker.trim() : task?.picker;
    return Boolean(picker);
  }

  function computeEffectiveState(task, allTasks = [], now = new Date()) {
    const scheduledAt = parseDateSafe(task?.scheduledDueAt);
    const readyAtIso = calcReadyAt(task || {});
    const readyAt = parseDateSafe(readyAtIso);

    let overdue = false;
    if (scheduledAt) {
      const intervalDays = Number(
        task?.recurrence?.intervalDays ?? task?.recurrence?.interval ?? 0,
      );
      if (task?.recurrence && intervalDays > 0) {
        const halfIntervalMs = (intervalDays / 2) * 24 * 60 * 60 * 1000;
        overdue = now.getTime() >= scheduledAt.getTime() + halfIntervalMs;
      } else {
        overdue = now.getTime() >= scheduledAt.getTime();
      }
    }

    const result = (effectiveState, overdueValue = overdue) => ({
      effectiveState,
      readyAt: readyAtIso,
      scheduledDueAt: scheduledAt ? scheduledAt.toISOString() : null,
      overdue: overdueValue,
    });

    if (task?.state === "Done") return result("Done", false);
    if (task?.state === "Blocked") return result("Blocked");
    if (task?.recurrence?.paused) return result("Suspended");
    if (readyAt && now.getTime() < readyAt.getTime()) {
      return result("Waiting", false);
    }
    if (anyDependencyUnresolved(allTasks, task)) return result("Suspended");

    if (!scheduledAt) {
      if (task?.state === "InProgress" && !hasActiveClaim(task)) {
        return result("Ready", false);
      }
      return result(task?.state || "Ready", false);
    }
    if (task?.state === "Suspended") return result("Suspended");
    if (task?.state === "InProgress" && hasActiveClaim(task)) {
      return result("InProgress");
    }
    return result("Ready");
  }

  return {
    TASK_STATES,
    anyDependencyUnresolved,
    calcReadyAt,
    computeEffectiveState,
    hasActiveClaim,
    parseDateSafe,
  };
});
