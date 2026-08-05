"use strict";

const STATES = ["Waiting", "Ready", "InProgress", "Blocked", "Suspended", "Done"];
const LABELS = {
    Waiting: "Waiting",
    Ready: "Ready",
    InProgress: "In Progress",
    Blocked: "Blocked",
    Suspended: "Suspended",
    Done: "Done",
};
const COLORS = {
    Waiting: "var(--waiting)",
    Ready: "var(--ready)",
    InProgress: "var(--progress)",
    Blocked: "var(--blocked)",
    Suspended: "var(--suspended)",
    Done: "var(--done)",
};
const MANUAL_TARGETS = new Set(["Ready", "InProgress", "Blocked", "Done"]);
const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const DEFAULT_PALETTE = "standard";

let tasks = [];
let wipLimits = {};
let editorTaskID = null;
let toastTimer = null;
let currentUser = null;
let events = null;
let refreshTimer = null;

const board = document.getElementById("board");
const editor = document.getElementById("editor");
const settings = document.getElementById("settings");
const backdrop = document.getElementById("backdrop");
const authScreen = document.getElementById("auth-screen");
const appShell = document.getElementById("app-shell");

async function request(path, options = {}) {
    const response = await fetch(path, {
        ...options,
        headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) {
        const error = new Error(body.error || `Request failed (${response.status})`);
        error.status = response.status;
        error.body = body;
        if (response.status === 401 && !path.startsWith("/api/auth/")) showAuth();
        throw error;
    }
    return body;
}

async function initialize() {
    try {
        const account = await request("/api/auth/me");
        if (account.authenticated) {
            await startApplication(account);
        } else {
            await showAuth();
        }
    } catch (error) {
        await showAuth();
        showToast(error.message);
    }
}

async function startApplication(account) {
    currentUser = account;
    authScreen.hidden = true;
    appShell.hidden = false;
    updateAccount(account);
    loadPalette(account.username);
    clearInterval(refreshTimer);
    await loadBoard();
    connectEvents();
    refreshTimer = setInterval(loadBoard, 15000);
}

async function showAuth() {
    currentUser = null;
    if (events) {
        events.close();
        events = null;
    }
    clearInterval(refreshTimer);
    refreshTimer = null;
    closePanels();
    appShell.hidden = true;
    authScreen.hidden = false;
    setConnectionState("disconnected");
    showLoginForm();
    try {
        const status = await request("/api/auth/registration");
        const registerButton = document.getElementById("show-register");
        registerButton.hidden = !status.enabled;
        registerButton.textContent = status.enabled ? "Create account" : "";
    } catch (error) {
        document.getElementById("show-register").hidden = true;
    }
    document.getElementById("login-username").focus();
}

function showLoginForm() {
    document.getElementById("auth-title").textContent = "Log in";
    document.getElementById("login-form").hidden = false;
    document.getElementById("register-form").hidden = true;
}

function showRegistrationForm() {
    document.getElementById("auth-title").textContent = "Create account";
    document.getElementById("login-form").hidden = true;
    document.getElementById("register-form").hidden = false;
    document.getElementById("register-username").focus();
}

async function login(event) {
    event.preventDefault();
    try {
        const account = await request("/api/auth/login", {
            method: "POST",
            body: JSON.stringify({
                username: document.getElementById("login-username").value.trim(),
                password: document.getElementById("login-password").value,
            }),
        });
        document.getElementById("login-password").value = "";
        await startApplication({ ...account, authenticated: true });
    } catch (error) {
        showToast(error.message);
    }
}

async function register(event) {
    event.preventDefault();
    const password = document.getElementById("register-password").value;
    if (password !== document.getElementById("register-confirm").value) {
        showToast("Passwords do not match");
        return;
    }
    try {
        const account = await request("/api/auth/register", {
            method: "POST",
            body: JSON.stringify({
                username: document.getElementById("register-username").value.trim(),
                password,
            }),
        });
        document.getElementById("register-form").reset();
        await startApplication({ ...account, authenticated: true });
    } catch (error) {
        showToast(error.message);
    }
}

async function logout() {
    try {
        await request("/api/auth/logout", { method: "POST", body: "{}" });
    } catch (error) {
        if (error.status !== 401) showToast(error.message);
    }
    await showAuth();
}

function connectEvents() {
    if (events) events.close();
    setConnectionState("connecting");
    events = new EventSource("/api/events");
    events.addEventListener("board", loadBoard);
    events.onopen = () => setConnectionState("connected");
    events.onerror = () => {
        if (currentUser) setConnectionState("connecting");
    };
}

function setConnectionState(state) {
    const status = document.getElementById("connection-status");
    status.className = `connection-status ${state}`;
    status.textContent = state === "connected" ? "Live" : state === "connecting" ? "Reconnecting" : "Offline";
}

async function loadBoard() {
    try {
        const [loadedTasks, loadedLimits, account] = await Promise.all([
            request("/api/tasks"),
            request("/api/wip-limits"),
            request("/api/auth/me"),
        ]);
        tasks = loadedTasks;
        wipLimits = loadedLimits;
        updateAccount(account);
        renderBoard();
    } catch (error) {
        if (error.status !== 401) showToast(error.message);
    }
}

function updateAccount(account) {
    currentUser = account;
    document.getElementById("current-user").textContent = account.username || "";
    document.getElementById("current-points").textContent = `${Number(account.points || 0)} pts`;
}

function renderBoard() {
    const groups = Object.fromEntries(STATES.map((state) => [state, []]));
    for (const task of tasks) {
        (groups[task.effectiveState] || groups.Ready).push(task);
    }
    for (const state of STATES) {
        groups[state].sort((left, right) => compareTasks(state, left, right));
    }
    board.replaceChildren(...STATES.map((state) => makeColumn(state, groups[state])));
}

function compareTasks(state, left, right) {
    if (state === "Done") {
        return dateValue(right.lastCompletedAt, 0) - dateValue(left.lastCompletedAt, 0);
    }
    if (state !== "Waiting") {
        const leftCritical = isTimeCriticalActive(left);
        const rightCritical = isTimeCriticalActive(right);
        if (leftCritical !== rightCritical) return leftCritical ? -1 : 1;
        if (leftCritical && rightCritical) {
            const dueDifference = dateValue(left.scheduledAt, Infinity) - dateValue(right.scheduledAt, Infinity);
            if (dueDifference) return dueDifference;
        }
        const priorityDifference = Number(right.priority || 0) - Number(left.priority || 0);
        if (priorityDifference) return priorityDifference;
    }
    if (left.scheduledAt && right.scheduledAt) {
        return new Date(left.scheduledAt) - new Date(right.scheduledAt);
    }
    if (left.scheduledAt) return -1;
    if (right.scheduledAt) return 1;
    return new Date(left.createdAt) - new Date(right.createdAt);
}

function makeColumn(state, columnTasks) {
    const column = element("section", "column");
    column.style.setProperty("--state-color", COLORS[state]);
    const header = element("header", "column-header");
    const limit = wipLimits[state];
    const count = element(
        "span",
        "column-count",
        limit === null || limit === undefined ? String(columnTasks.length) : `${columnTasks.length} / ${limit}`,
    );
    if (limit !== null && limit !== undefined && columnTasks.length > limit) count.classList.add("over-limit");
    header.append(element("span", "", LABELS[state]), count);
    const list = element("div", "column-list");
    list.dataset.state = state;
    for (const task of columnTasks) list.append(makeCard(task));
    enableDropTarget(list);
    column.append(header, list);
    return column;
}

function makeCard(task) {
    const draggable = ["Ready", "InProgress", "Blocked"].includes(task.effectiveState);
    const card = element("article", "card");
    if (isTimeCriticalActive(task)) card.classList.add("time-critical");
    if (task.overdue) card.classList.add("overdue");
    card.style.setProperty("--state-color", COLORS[task.effectiveState]);
    card.draggable = draggable;
    card.dataset.id = task.id;
    if (!["Waiting", "Done"].includes(task.effectiveState)) {
        const priority = element("span", "priority", String(task.priority || 1));
        priority.style.background = priorityColor(task.priority);
        priority.title = "Automatic priority";
        card.append(priority);
    }
    card.append(element("h3", "card-title", task.title));
    if (task.description) {
        card.append(element("p", "card-description", truncate(task.description, 150)));
    }
    if (task.effectiveState === "Blocked" && task.blockNote) card.append(element("p", "card-note", task.blockNote));

    const metadata = element("div", "metadata");
    if (task.scheduledAt) metadata.append(element("span", "", `Due ${formatDate(task.scheduledAt)}`));
    if (task.readyAt && task.effectiveState === "Waiting") metadata.append(element("span", "", `Ready ${formatDate(task.readyAt)}`));
    if (task.deadline) metadata.append(element("span", "", `Deadline ${formatDate(task.deadline)}`));
    if (task.overdue) metadata.append(element("span", "overdue-label", "Overdue"));
    if (task.timeCritical) metadata.append(element("span", "", "Time critical"));
    if (task.claimedBy) {
        const ownerLabel = task.effectiveState === "Done" ? "Completed by" : "Claimed by";
        metadata.append(element("span", "", `${ownerLabel} ${task.claimedBy}`));
    }
    if (task.dependencies.length) metadata.append(element("span", "", `${task.dependencies.length} dependencies`));
    if (task.recurrence.kind !== "none") {
        const recurrenceText = task.recurrence.weekdays?.length
            ? `${task.recurrence.kind} on ${task.recurrence.weekdays.map((weekday) => WEEKDAYS[weekday]).join(", ")}`
            : `${task.recurrence.kind} every ${task.recurrence.days} days`;
        metadata.append(element("span", "", recurrenceText));
    }
    if (task.remedyFor) metadata.append(element("span", "", "Remedy task"));
    card.append(metadata);

    const actions = element("div", "card-actions");
    for (const [label, action, className] of actionsFor(task)) {
        const button = element("button", className || "", label);
        button.type = "button";
        button.addEventListener("click", async (event) => {
            event.stopPropagation();
            if (action === "remedy") await createRemedy(task);
            else await runAction(task, action);
        });
        actions.append(button);
    }
    if (actions.childElementCount) card.append(actions);

    card.addEventListener("click", () => openEditor(task));
    card.addEventListener("dragstart", (event) => {
        if (!draggable) return event.preventDefault();
        event.dataTransfer.setData("text/plain", task.id);
        event.dataTransfer.effectAllowed = "move";
        card.classList.add("dragging");
    });
    card.addEventListener("dragend", () => card.classList.remove("dragging"));
    return card;
}

function actionsFor(task) {
    switch (task.effectiveState) {
        case "Ready":
            return [["Claim", "claim"], ["Complete", "complete", "secondary"], ["Block", "block", "danger"]];
        case "InProgress":
            return [["Release", "release", "secondary"], ["Complete", "complete"], ["Block", "block", "danger"]];
        case "Blocked":
            return [["Remedy", "remedy"], ["Unblock", "unblock", "secondary"]];
        case "Done":
            return task.canUndo ? [["Undo", "undo", "secondary"]] : [];
        default:
            return [];
    }
}

async function runAction(task, action) {
    let note = "";
    if (action === "block") {
        const response = window.prompt("What is blocking this task?", task.blockNote || "");
        if (response === null) return;
        note = response;
    }
    try {
        await request(`/api/tasks/${encodeURIComponent(task.id)}/${action}`, {
            method: "POST",
            body: JSON.stringify({ version: task.version, note }),
        });
        await loadBoard();
    } catch (error) {
        showToast(error.message);
        if (error.status === 409) await loadBoard();
    }
}

async function createRemedy(task) {
    const title = window.prompt("Remedy title", `Remedy for ${task.title}`);
    if (title === null) return;
    try {
        await request(`/api/tasks/${encodeURIComponent(task.id)}/remedy`, {
            method: "POST",
            body: JSON.stringify({ title: title.trim(), version: task.version }),
        });
        await loadBoard();
    } catch (error) {
        showToast(error.message);
        if (error.status === 409) await loadBoard();
    }
}

function enableDropTarget(list) {
    const destination = list.dataset.state;
    if (!MANUAL_TARGETS.has(destination)) return;
    list.addEventListener("dragover", (event) => {
        event.preventDefault();
        list.classList.add("drop-target");
    });
    list.addEventListener("dragleave", () => list.classList.remove("drop-target"));
    list.addEventListener("drop", async (event) => {
        event.preventDefault();
        list.classList.remove("drop-target");
        const task = tasks.find((candidate) => candidate.id === event.dataTransfer.getData("text/plain"));
        if (!task || task.effectiveState === destination) return;
        if (destination === "InProgress" && task.effectiveState === "Ready") return runAction(task, "claim");
        if (destination === "Ready" && task.effectiveState === "InProgress") return runAction(task, "release");
        if (destination === "Ready" && task.effectiveState === "Blocked") return runAction(task, "unblock");
        if (destination === "Blocked" && ["Ready", "InProgress"].includes(task.effectiveState)) {
            return runAction(task, "block");
        }
        if (destination === "Done" && task.effectiveState === "InProgress") {
            return runAction(task, "complete");
        }
        if (destination === "Done" && task.effectiveState === "Ready") {
            return runAction(task, "complete");
        }
        showToast(`Move ${LABELS[task.effectiveState]} to ${LABELS[destination]} using its command first`);
    });
}

function openEditor(task = null) {
    closeSettings();
    editorTaskID = task?.id || null;
    document.getElementById("editor-title").textContent = task ? "Edit task" : "New task";
    document.getElementById("task-title").value = task?.title || "";
    document.getElementById("task-description").value = task?.description || "";
    document.getElementById("task-block-note").value = task?.blockNote || "";
    document.getElementById("block-note-wrap").hidden = !task || (task.effectiveState !== "Blocked" && !task.blockNote);
    document.getElementById("delete-task").hidden = !task;
    document.getElementById("task-scheduled").value = task?.scheduledAt ? toLocalInput(task.scheduledAt) : "";
    document.getElementById("task-lead").value = String(task?.leadDays || 0);
    document.getElementById("task-recurrence").value = task?.recurrence.kind || "none";
    document.getElementById("task-recurrence-days").value = task
        ? (task.recurrence.days ? String(task.recurrence.days) : "")
        : "30";
    document.getElementById("task-paused").checked = Boolean(task?.recurrence.paused);
    const selectedWeekdays = new Set(task?.recurrence.weekdays || []);
    for (const checkbox of document.querySelectorAll("#weekday-options input")) {
        checkbox.checked = selectedWeekdays.has(Number(checkbox.value));
    }
    document.getElementById("task-time-critical").checked = Boolean(task?.timeCritical);
    renderTaskContext(task);
    renderDependencies(task);
    updateRecurrenceControls();
    backdrop.hidden = false;
    editor.classList.add("open");
    editor.setAttribute("aria-hidden", "false");
    document.getElementById("task-title").focus();
}

function closeEditor() {
    editorTaskID = null;
    editor.classList.remove("open");
    editor.setAttribute("aria-hidden", "true");
    if (!settings.classList.contains("open")) backdrop.hidden = true;
}

function renderTaskContext(task) {
    const context = document.getElementById("task-context");
    if (!task) {
        context.hidden = true;
        context.replaceChildren();
        return;
    }
    const values = [
        LABELS[task.effectiveState],
        `Priority ${task.priority || 1}`,
        `Created ${formatDate(task.createdAt)}`,
        formatAge(task.createdAt),
    ];
    if (task.readyAt) values.push(`Ready ${formatDate(task.readyAt)}`);
    if (task.deadline) values.push(`Deadline ${formatDate(task.deadline)}`);
    if (task.overdue) values.push("Overdue");
    if (task.claimedBy) values.push(`Claimed by ${task.claimedBy}`);
    if (task.createdBy) values.push(`Created by ${task.createdBy}`);
    if (typeof task.pointsSnapshot === "number") {
        const state = task.pointsSnapshotAwarded ? "awarded" : "frozen";
        values.push(`${task.pointsSnapshot} point snapshot (${state})`);
    }
    if (task.awarded) values.push(`Awarded ${task.awarded.points} points to ${task.awarded.to}`);
    if (task.lastCompletedAt) values.push(`Completed ${formatDate(task.lastCompletedAt)}`);
    if (task.remedyFor) {
        const parent = tasks.find((candidate) => candidate.id === task.remedyFor);
        values.push(parent ? `Remedy for ${parent.title}` : "Remedy task");
    }
    context.replaceChildren(...values.map((value) => element("span", "", value)));
    context.hidden = false;
}

function renderDependencies(task) {
    const container = document.getElementById("dependency-list");
    const selected = new Set(task?.dependencies || []);
    const candidates = tasks.filter((candidate) => candidate.id !== task?.id);
    if (!candidates.length) {
        container.textContent = "No available tasks";
        return;
    }
    container.replaceChildren(...candidates.map((candidate) => {
        const label = element("label", "dependency-option");
        const checkbox = document.createElement("input");
        checkbox.type = "checkbox";
        checkbox.value = candidate.id;
        checkbox.checked = selected.has(candidate.id);
        label.append(checkbox, element("span", "", candidate.title));
        return label;
    }));
}

async function saveEditor(event) {
    event.preventDefault();
    const existing = editorTaskID ? tasks.find((task) => task.id === editorTaskID) : null;
    const recurrenceKind = document.getElementById("task-recurrence").value;
    const scheduledValue = document.getElementById("task-scheduled").value;
    const selectedDependencies = Array.from(
        document.querySelectorAll("#dependency-list input:checked"),
        (input) => input.value,
    );
    const cleanupRemedies = [];
    if (existing) {
        const selected = new Set(selectedDependencies);
        for (const dependencyID of existing.dependencies || []) {
            if (selected.has(dependencyID)) continue;
            const dependency = tasks.find((task) => task.id === dependencyID);
            const usedElsewhere = tasks.some((task) =>
                task.id !== existing.id && (task.dependencies || []).includes(dependencyID));
            if (dependency?.remedyFor === existing.id && !usedElsewhere && window.confirm(
                `Delete the now-unused remedy task "${dependency.title}"?`,
            )) {
                cleanupRemedies.push(dependencyID);
            }
        }
    }
    const payload = {
        title: document.getElementById("task-title").value.trim(),
        description: document.getElementById("task-description").value.trim(),
        blockNote: document.getElementById("task-block-note").value.trim(),
        timeCritical: document.getElementById("task-time-critical").checked,
        scheduledAt: scheduledValue ? new Date(scheduledValue).toISOString() : null,
        deadline: existing?.deadline || null,
        leadDays: Number(document.getElementById("task-lead").value || 0),
        recurrence: {
            kind: recurrenceKind,
            days: recurrenceKind === "none" ? 0 : Number(document.getElementById("task-recurrence-days").value || 0),
            weekdays: recurrenceKind === "anchored"
                ? Array.from(document.querySelectorAll("#weekday-options input:checked"), (input) => Number(input.value))
                : [],
            paused: recurrenceKind !== "none" && document.getElementById("task-paused").checked,
        },
        dependencies: selectedDependencies,
        cleanupRemedies,
        version: existing?.version || 0,
    };
    try {
        if (existing) {
            await request(`/api/tasks/${encodeURIComponent(existing.id)}`, {
                method: "PATCH",
                body: JSON.stringify(payload),
            });
        } else {
            await request("/api/tasks", { method: "POST", body: JSON.stringify(payload) });
        }
        closeEditor();
        await loadBoard();
    } catch (error) {
        showToast(error.message);
        if (error.status === 409) await loadBoard();
    }
}

function openSettings() {
    closeEditor();
    for (const state of STATES) {
        const limit = wipLimits[state];
        document.getElementById(`limit-${state}`).value = limit === null || limit === undefined ? "" : String(limit);
    }
    document.getElementById("palette-select").value = getCurrentPalette();
    backdrop.hidden = false;
    settings.classList.add("open");
    settings.setAttribute("aria-hidden", "false");
    document.getElementById("limit-InProgress").focus();
}

function closeSettings() {
    settings.classList.remove("open");
    settings.setAttribute("aria-hidden", "true");
    if (!editor.classList.contains("open")) backdrop.hidden = true;
}

function closePanels() {
    closeEditor();
    closeSettings();
    backdrop.hidden = true;
}

async function saveSettings(event) {
    event.preventDefault();
    const payload = {};
    for (const state of STATES) {
        const value = document.getElementById(`limit-${state}`).value;
        payload[state] = value === "" ? null : Number(value);
    }
    try {
        wipLimits = await request("/api/wip-limits", {
            method: "PATCH",
            body: JSON.stringify(payload),
        });
        const palette = document.getElementById("palette-select").value || DEFAULT_PALETTE;
        applyPalette(palette);
        savePalette(palette);
        closeSettings();
        renderBoard();
    } catch (error) {
        showToast(error.message);
    }
}

async function changePassword(event) {
    event.preventDefault();
    const currentPassword = document.getElementById("current-password").value;
    const newPassword = document.getElementById("new-password").value;
    if (newPassword !== document.getElementById("confirm-password").value) {
        showToast("New passwords do not match");
        return;
    }
    try {
        await request("/api/auth/change-password", {
            method: "POST",
            body: JSON.stringify({ currentPassword, newPassword }),
        });
        document.getElementById("password-form").reset();
        closeSettings();
        connectEvents();
        showToast("Password changed");
    } catch (error) {
        showToast(error.message);
    }
}

async function openHistory() {
    try {
        const result = await request("/api/account/completions");
        renderHeatmap(result.entries || []);
        document.getElementById("history-dialog").showModal();
    } catch (error) {
        showToast(error.message);
    }
}

function renderHeatmap(entries) {
    const today = startOfLocalDay(new Date());
    const endSunday = new Date(today);
    endSunday.setDate(endSunday.getDate() - endSunday.getDay());
    const start = new Date(endSunday);
    start.setDate(start.getDate() - 52 * 7);
    const counts = new Map();
    for (const entry of entries) {
        const occurredAt = new Date(entry.occurredAt);
        if (Number.isNaN(occurredAt.getTime())) continue;
        const key = localDateKey(occurredAt);
        counts.set(key, (counts.get(key) || 0) + 1);
    }

    let maxCount = 0;
    for (let week = 0; week < 53; week++) {
        for (let day = 0; day < 7; day++) {
            const date = new Date(start);
            date.setDate(start.getDate() + week * 7 + day);
            if (date <= today) maxCount = Math.max(maxCount, counts.get(localDateKey(date)) || 0);
        }
    }

    const cells = [];
    for (let week = 0; week < 53; week++) {
        for (let day = 0; day < 7; day++) {
            const date = new Date(start);
            date.setDate(start.getDate() + week * 7 + day);
            const count = date <= today ? counts.get(localDateKey(date)) || 0 : 0;
            const cell = element("span", `heatmap-cell level-${heatmapLevel(count, maxCount)}`);
            cell.title = date <= today
                ? `${date.toLocaleDateString()}: ${count} completion${count === 1 ? "" : "s"}`
                : "";
            cells.push(cell);
        }
    }
    document.getElementById("heatmap-grid").replaceChildren(...cells);

    const oneYearAgo = new Date(today);
    oneYearAgo.setFullYear(oneYearAgo.getFullYear() - 1);
    const activeEntries = entries.filter((entry) => {
        const occurredAt = new Date(entry.occurredAt);
        return occurredAt >= oneYearAgo && occurredAt <= todayWithEndTime(today);
    });
    let currentStreak = 0;
    for (let offset = 0; ; offset++) {
        const date = new Date(today);
        date.setDate(date.getDate() - offset);
        if ((counts.get(localDateKey(date)) || 0) === 0) break;
        currentStreak++;
    }
    let longestStreak = 0;
    let runningStreak = 0;
    for (let date = new Date(oneYearAgo); date <= today; date.setDate(date.getDate() + 1)) {
        if ((counts.get(localDateKey(date)) || 0) > 0) {
            runningStreak++;
            longestStreak = Math.max(longestStreak, runningStreak);
        } else {
            runningStreak = 0;
        }
    }
    document.getElementById("history-summary").textContent =
        `${activeEntries.length} completions in the last year | Current streak ${currentStreak} days | Longest ${longestStreak} days`;
}

function heatmapLevel(count, maxCount) {
    if (!count) return 0;
    if (maxCount <= 1) return 4;
    return Math.min(4, Math.max(1, Math.ceil((count / maxCount) * 4)));
}

function startOfLocalDay(date) {
    return new Date(date.getFullYear(), date.getMonth(), date.getDate());
}

function todayWithEndTime(date) {
    return new Date(date.getFullYear(), date.getMonth(), date.getDate(), 23, 59, 59, 999);
}

function localDateKey(date) {
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    return `${year}-${month}-${day}`;
}

async function deleteEditorTask() {
    const task = editorTaskID ? tasks.find((candidate) => candidate.id === editorTaskID) : null;
    if (!task || !window.confirm(`Delete task "${task.title}"? This cannot be undone.`)) return;
    try {
        await deleteTask(task, false);
    } catch (error) {
        const dependents = error.body?.dependents || [];
        if (error.status !== 409 || !dependents.length) {
            showToast(error.message);
            if (error.status === 409) await loadBoard();
            return;
        }
        const names = dependents.map((dependent) => dependent.title).join("\n- ");
        const confirmed = window.confirm(
            `Other tasks depend on this task:\n- ${names}\n\nDelete it and remove those dependencies?`,
        );
        if (!confirmed) return;
        try {
            await deleteTask(task, true);
        } catch (retryError) {
            showToast(retryError.message);
            if (retryError.status === 409) await loadBoard();
        }
    }
}

async function deleteTask(task, force) {
    await request(`/api/tasks/${encodeURIComponent(task.id)}`, {
        method: "DELETE",
        body: JSON.stringify({ version: task.version, force }),
    });
    closeEditor();
    await loadBoard();
}

async function quickAdd(event) {
    event.preventDefault();
    const input = document.getElementById("quick-title");
    const title = input.value.trim();
    if (!title) return;
    try {
        await request("/api/tasks", { method: "POST", body: JSON.stringify({ title }) });
        input.value = "";
        await loadBoard();
    } catch (error) {
        showToast(error.message);
    }
}

function updateRecurrenceControls() {
    const kind = document.getElementById("task-recurrence").value;
    const recurring = kind !== "none";
    document.getElementById("task-recurrence-days").disabled = !recurring;
    document.getElementById("task-recurrence-days").required = kind === "rolling";
    document.getElementById("task-paused").disabled = !recurring;
    document.getElementById("weekday-options").hidden = kind !== "anchored";
}

function element(tag, className = "", text = null) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== null) node.textContent = text;
    return node;
}

function truncate(value, max) {
    return value.length <= max ? value : `${value.slice(0, max - 3)}...`;
}

function formatDate(value) {
    return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function formatAge(value) {
    const createdAt = new Date(value);
    if (Number.isNaN(createdAt.getTime())) return "Age unknown";
    const minutes = Math.max(0, Math.floor((Date.now() - createdAt.getTime()) / 60000));
    if (minutes < 1) return "Just created";
    if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} old`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} old`;
    const days = Math.floor(hours / 24);
    return `${days} day${days === 1 ? "" : "s"} old`;
}

function getCurrentPalette() {
    return document.documentElement.dataset.palette || DEFAULT_PALETTE;
}

function applyPalette(palette) {
    if (!palette || palette === DEFAULT_PALETTE) delete document.documentElement.dataset.palette;
    else document.documentElement.dataset.palette = palette;
}

function paletteStorageKey(username = currentUser?.username) {
    return `kanban.palette.${username || "default"}`;
}

function loadPalette(username) {
    try {
        applyPalette(localStorage.getItem(paletteStorageKey(username)) || DEFAULT_PALETTE);
    } catch (error) {
        applyPalette(DEFAULT_PALETTE);
    }
}

function savePalette(palette) {
    try {
        localStorage.setItem(paletteStorageKey(), palette);
    } catch (error) {
        showToast("Palette preference could not be saved");
    }
}

function dateValue(value, fallback) {
    if (!value) return fallback;
    const parsed = new Date(value).getTime();
    return Number.isNaN(parsed) ? fallback : parsed;
}

function priorityColor(value) {
    const priority = Number(value) || 0;
    if (priority <= 33) return "var(--ready)";
    if (priority <= 66) return "var(--waiting)";
    return "var(--blocked)";
}

function isTimeCriticalActive(task) {
    if (!task.timeCritical || !task.scheduledAt || ["Waiting", "Done"].includes(task.effectiveState)) return false;
    const due = new Date(task.scheduledAt);
    if (Number.isNaN(due.getTime())) return false;
    const today = new Date();
    const dueDate = new Date(due.getFullYear(), due.getMonth(), due.getDate()).getTime();
    const todayDate = new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime();
    return dueDate <= todayDate;
}

function toLocalInput(value) {
    const date = new Date(value);
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
    return local.toISOString().slice(0, 16);
}

function showToast(message) {
    const toast = document.getElementById("toast");
    toast.textContent = message;
    toast.classList.add("visible");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.remove("visible"), 4000);
}

document.getElementById("quick-add").addEventListener("submit", quickAdd);
document.getElementById("new-task").addEventListener("click", () => openEditor());
document.getElementById("settings-button").addEventListener("click", openSettings);
document.getElementById("task-form").addEventListener("submit", saveEditor);
document.getElementById("settings-form").addEventListener("submit", saveSettings);
document.getElementById("password-form").addEventListener("submit", changePassword);
document.getElementById("close-editor").addEventListener("click", closeEditor);
document.getElementById("cancel-editor").addEventListener("click", closeEditor);
document.getElementById("delete-task").addEventListener("click", deleteEditorTask);
document.getElementById("close-settings").addEventListener("click", closeSettings);
document.getElementById("cancel-settings").addEventListener("click", closeSettings);
document.getElementById("backdrop").addEventListener("click", closePanels);
document.getElementById("task-recurrence").addEventListener("change", updateRecurrenceControls);
document.getElementById("login-form").addEventListener("submit", login);
document.getElementById("register-form").addEventListener("submit", register);
document.getElementById("show-register").addEventListener("click", showRegistrationForm);
document.getElementById("show-login").addEventListener("click", showLoginForm);
document.getElementById("logout-button").addEventListener("click", logout);
document.getElementById("account-summary").addEventListener("click", openHistory);
document.getElementById("close-history").addEventListener("click", () => document.getElementById("history-dialog").close());
document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") closePanels();
});

initialize();
