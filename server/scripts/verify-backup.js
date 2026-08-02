#!/usr/bin/env node
"use strict";

const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

const backupDir = process.argv[2] && path.resolve(process.argv[2]);
if (!backupDir) {
  console.error("Usage: npm run backup:verify -- /path/to/kanban-backup");
  process.exit(1);
}

const manifestPath = path.join(backupDir, "manifest.json");
const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
if (manifest.version !== 1 || !Array.isArray(manifest.files)) {
  throw new Error("Unsupported backup manifest");
}

for (const entry of manifest.files) {
  const filePath = path.join(backupDir, entry.name);
  const content = fs.readFileSync(filePath);
  const digest = crypto.createHash("sha256").update(content).digest("hex");
  if (digest !== entry.sha256 || content.length !== entry.bytes) {
    throw new Error(`Backup verification failed for ${entry.name}`);
  }
  if (entry.name.endsWith(".json")) JSON.parse(content.toString("utf8"));
}

console.log(`Verified ${manifest.files.length} files in ${backupDir}`);
