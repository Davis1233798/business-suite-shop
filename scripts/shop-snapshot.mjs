import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { pathToFileURL } from "node:url";

// Full-database offline snapshot. SQLite VACUUM INTO reads a transactionally
// consistent snapshot including committed WAL pages; identity/audit/outbox are retained.
export function snapshot(source, target) {
  const src = path.resolve(source), dest = path.resolve(target);
  if (src === dest || !fs.lstatSync(src).isFile()) throw new Error("Source must be an existing regular file distinct from target");
  // The temporary file is on the same filesystem as the destination. It is
  // private on POSIX; on Windows the parent directory ACL controls access.
  const temporary = path.join(path.dirname(dest), `.suite-snapshot-${randomBytes(16).toString("hex")}.tmp`);
  const fd = fs.openSync(temporary, "wx", 0o600);
  fs.closeSync(fd);
  let input, output;
  try {
    input = new DatabaseSync(src, { readOnly: true });
    const marker = input.prepare("SELECT version FROM shop_vinext_schema").get();
    if (!marker || marker.version !== 1) throw new Error("Not a supported SHOP Vinext database");
    if (input.prepare("PRAGMA integrity_check").get().integrity_check !== "ok") throw new Error("Source integrity check failed");
    input.prepare("VACUUM INTO ?").run(temporary);
    output = new DatabaseSync(temporary, { readOnly: true });
    if (output.prepare("PRAGMA integrity_check").get().integrity_check !== "ok") throw new Error("Snapshot integrity check failed");
    if (output.prepare("SELECT version FROM shop_vinext_schema").get()?.version !== 1) throw new Error("Snapshot marker check failed");
    output.close(); output = undefined;
    // Hard-link creation is atomic and fails if destination already exists.
    // Both names refer to the verified inode until the temporary name is removed.
    fs.linkSync(temporary, dest);
  } finally {
    output?.close(); input?.close();
    fs.rmSync(temporary, { force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const [, , command, source, target, flag] = process.argv;
  if (!["backup", "restore"].includes(command) || !source || !target || flag !== "--confirm-plaintext") {
    console.error("Usage: node scripts/shop-snapshot.mjs backup|restore SOURCE NEW_TARGET --confirm-plaintext\nSnapshot contains ALL tenants and is plaintext. Restore only to a NEW file; stop the app before switching its database path.");
    process.exitCode = 2;
  } else {
    try { snapshot(source, target); console.log("Snapshot verified. Source retained; target is plaintext and includes all tenants."); }
    catch (error) { console.error(`Snapshot failed: ${error.message}`); process.exitCode = 1; }
  }
}
