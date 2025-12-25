// test_recompute_cli.js
// Node 18+ (uses global fetch). If using older Node, install node-fetch and adjust accordingly.

const base = "http://localhost:3000";

async function fetchTasks() {
  const res = await fetch(`${base}/tasks`);
  if (!res.ok) throw new Error(`GET /tasks failed: ${res.status}`);
  return res.json();
}

function printSnapshot(label, tasks) {
  console.log(`\n=== ${label} ===`);
  if (!tasks.length) {
    console.log("(no tasks)");
    return;
  }
  console.log("id\tpriority\ttitle");
  for (const t of tasks) {
    const p = (typeof t.priority === "number") ? t.priority : "(none)";
    console.log(`${t.id}\t${p}\t${t.title}`);
  }
}

async function forceRecompute() {
  const res = await fetch(`${base}/recompute`, { method: "POST" });
  if (!res.ok) throw new Error(`POST /recompute failed: ${res.status}`);
  return res.json();
}

(async () => {
  try {
    const before = await fetchTasks();
    printSnapshot("Before recompute", before);

    console.log("\nForcing recompute...");
    const r = await forceRecompute();
    console.log("Recompute response:", r);

    const after = await fetchTasks();
    printSnapshot("After recompute", after);
  } catch (err) {
    console.error("Error:", err.message);
    process.exit(1);
  }
})();

