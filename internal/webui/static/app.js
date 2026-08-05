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

let tasks = [];
let editorTaskID = null;
let toastTimer = null;

const board = document.getElementById("board");
const editor = document.getElementById("editor");
const backdrop = document.getElementById("backdrop");

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
        throw error;
    }
    return body;
}

async function loadBoard() {
    try {
        tasks = await request("/api/tasks");
        renderBoard();
    } catch (error) {
        showToast(error.message);
    }
}

function renderBoard() {
    const groups = Object.fromEntries(STATES.map((state) => [state, []]));
    for (const task of tasks) {
        (groups[task.effectiveState] || groups.Ready).push(task);
    }
    for (const state of STATES) {
        groups[state].sort(compareTasks);
    }
    board.replaceChildren(...STATES.map((state) => makeColumn(state, groups[state])));
}

function compareTasks(left, right) {
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
    header.append(
        element("span", "", LABELS[state]),
        element("span", "column-count", String(columnTasks.length)),
    );
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
    card.style.setProperty("--state-color", COLORS[task.effectiveState]);
    card.draggable = draggable;
    card.dataset.id = task.id;
    card.append(element("h3", "card-title", task.title));
    if (task.description) {
        card.append(element("p", "card-description", truncate(task.description, 150)));
    }
    if (task.blockNote) card.append(element("p", "card-note", task.blockNote));

    const metadata = element("div", "metadata");
    if (task.scheduledAt) metadata.append(element("span", "", formatDate(task.scheduledAt)));
    if (task.claimedBy) metadata.append(element("span", "", `Claimed by ${task.claimedBy}`));
    if (task.dependencies.length) metadata.append(element("span", "", `${task.dependencies.length} dependencies`));
    if (task.recurrence.kind !== "none") {
        metadata.append(element("span", "", `${task.recurrence.kind} every ${task.recurrence.days} days`));
    }
    card.append(metadata);

    const actions = element("div", "card-actions");
    for (const [label, action, className] of actionsFor(task)) {
        const button = element("button", className || "", label);
        button.type = "button";
        button.addEventListener("click", async (event) => {
            event.stopPropagation();
            await runAction(task, action);
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
            return [["Claim", "claim"], ["Block", "block", "danger"]];
        case "InProgress":
            return [["Release", "release", "secondary"], ["Complete", "complete"], ["Block", "block", "danger"]];
        case "Blocked":
            return [["Unblock", "unblock"]];
        case "Done":
            return task.canUndo ? [["Undo", "undo", "secondary"]] : [];
        default:
            return [];
    }
}

async function runAction(task, action) {
    let note = "";
    if (action === "block") {
        note = window.prompt("What is blocking this task?", task.blockNote || "") ?? "";
        if (!note && task.effectiveState !== "Blocked") return;
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
        if (destination === "Done" && ["Ready", "InProgress"].includes(task.effectiveState)) {
            return runAction(task, "complete");
        }
        showToast(`Move ${LABELS[task.effectiveState]} to ${LABELS[destination]} using its command first`);
    });
}

function openEditor(task = null) {
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
    document.getElementById("task-recurrence-days").value = String(task?.recurrence.days || 30);
    document.getElementById("task-paused").checked = Boolean(task?.recurrence.paused);
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
    backdrop.hidden = true;
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
    const payload = {
        title: document.getElementById("task-title").value.trim(),
        description: document.getElementById("task-description").value.trim(),
        blockNote: document.getElementById("task-block-note").value.trim(),
        scheduledAt: scheduledValue ? new Date(scheduledValue).toISOString() : null,
        leadDays: Number(document.getElementById("task-lead").value || 0),
        recurrence: {
            kind: recurrenceKind,
            days: recurrenceKind === "none" ? 0 : Number(document.getElementById("task-recurrence-days").value || 0),
            paused: recurrenceKind !== "none" && document.getElementById("task-paused").checked,
        },
        dependencies: Array.from(document.querySelectorAll("#dependency-list input:checked"), (input) => input.value),
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
    const recurring = document.getElementById("task-recurrence").value !== "none";
    document.getElementById("task-recurrence-days").disabled = !recurring;
    document.getElementById("task-paused").disabled = !recurring;
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
document.getElementById("task-form").addEventListener("submit", saveEditor);
document.getElementById("close-editor").addEventListener("click", closeEditor);
document.getElementById("cancel-editor").addEventListener("click", closeEditor);
document.getElementById("delete-task").addEventListener("click", deleteEditorTask);
document.getElementById("backdrop").addEventListener("click", closeEditor);
document.getElementById("task-recurrence").addEventListener("change", updateRecurrenceControls);
document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && editor.classList.contains("open")) closeEditor();
});

const events = new EventSource("/api/events");
events.addEventListener("board", loadBoard);
events.onerror = () => showToast("Live connection interrupted; reconnecting");
setInterval(loadBoard, 60000);
loadBoard();
