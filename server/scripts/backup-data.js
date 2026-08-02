#!/usr/bin/env node
"use strict";

const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

const serverDir = path.resolve(__dirname, "..");
const sourceDir = path.resolve(
  process.env.KANBAN_DATA_DIR || path.join(serverDir, "data"),
);
const destinationRoot = process.argv[2] || process.env.KANBAN_BACKUP_DEST;
const retention = Number(process.env.KANBAN_BACKUP_RETENTION || 30);
const files = ["tasks.json", "users.json", "wip_limits.json"];

function fail(message) {
  console.error(message);
  process.exit(1);
}

function hashFile(filePath) {
  return crypto.createHash("sha256").update(fs.readFileSync(filePath)).digest("hex");
}

if (!destinationRoot) {
  fail("Set KANBAN_BACKUP_DEST or pass a destination directory as the first argument.");
}
if (!Number.isInteger(retention) || retention < 2 || retention > 1000) {
  fail("KANBAN_BACKUP_RETENTION must be an integer from 2 through 1000.");
}

const destination = path.resolve(destinationRoot);
if (
  destination === path.parse(destination).root ||
  destination === sourceDir ||
  destination.startsWith(`${sourceDir}${path.sep}`)
) {
  fail("Backup destination must be a dedicated directory outside the live data directory.");
}

for (const fileName of files.slice(0, 3)) {
  if (!fs.existsSync(path.join(sourceDir, fileName))) {
    fail(`Required data file is missing: ${path.join(sourceDir, fileName)}`);
  }
}

fs.mkdirSync(destination, { recursive: true, mode: 0o700 });
fs.chmodSync(destination, 0o700);

const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
const finalDir = path.join(destination, `kanban-${timestamp}`);
const tempDir = `${finalDir}.tmp-${process.pid}`;
fs.mkdirSync(tempDir, { mode: 0o700 });

try {
  const manifest = {
    version: 1,
    createdAt: new Date().toISOString(),
    sourceDir,
    files: [],
  };
  for (const fileName of files) {
    const sourcePath = path.join(sourceDir, fileName);
    if (!fs.existsSync(sourcePath)) continue;
    const destinationPath = path.join(tempDir, fileName);
    fs.copyFileSync(sourcePath, destinationPath, fs.constants.COPYFILE_EXCL);
    fs.chmodSync(destinationPath, 0o600);
    JSON.parse(
      fileName.endsWith(".json")
        ? fs.readFileSync(destinationPath, "utf8")
        : JSON.stringify({ valid: true }),
    );
    manifest.files.push({
      name: fileName,
      bytes: fs.statSync(destinationPath).size,
      sha256: hashFile(destinationPath),
    });
  }
  fs.writeFileSync(
    path.join(tempDir, "manifest.json"),
    `${JSON.stringify(manifest, null, 2)}\n`,
    { encoding: "utf8", mode: 0o600, flag: "wx" },
  );
  fs.renameSync(tempDir, finalDir);

  const latestLink = path.join(destination, "latest");
  const temporaryLink = path.join(destination, `.latest-${process.pid}`);
  try {
    fs.unlinkSync(temporaryLink);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  fs.symlinkSync(path.basename(finalDir), temporaryLink, "dir");
  fs.renameSync(temporaryLink, latestLink);

  const backups = fs
    .readdirSync(destination, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && /^kanban-\d{4}-/.test(entry.name))
    .map((entry) => entry.name)
    .sort()
    .reverse();
  for (const stale of backups.slice(retention)) {
    fs.rmSync(path.join(destination, stale), { recursive: true });
  }
  console.log(finalDir);
} catch (error) {
  fs.rmSync(tempDir, { recursive: true, force: true });
  throw error;
}
