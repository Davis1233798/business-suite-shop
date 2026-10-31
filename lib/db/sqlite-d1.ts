import { DatabaseSync } from "node:sqlite";
import fs from "node:fs";
import path from "node:path";

let defaultDb: DatabaseSync | null = null;
let defaultD1: any = null;

export function createSqliteD1(dbPath: string = ":memory:"): any {
  if (dbPath !== ":memory:") {
    const dir = path.dirname(dbPath);
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }
  }

  const db = new DatabaseSync(dbPath);

  // Enable foreign keys, WAL mode, and busy timeout if on disk
  try {
    db.exec("PRAGMA foreign_keys = ON;");
    db.exec("PRAGMA busy_timeout = 10000;");
    if (dbPath !== ":memory:") {
      db.exec("PRAGMA journal_mode = WAL;");
    }
  } catch {}

  // One writer owns schema inspection and all migrations. Station processes
  // share this local volume, so no other process may observe a partial schema.
  db.exec("BEGIN IMMEDIATE");
  try {
    const tableCheck = db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='organizations'").get();
    if (!tableCheck) {
      db.exec(fs.readFileSync(path.resolve(process.cwd(), "drizzle/shop_initial.sql"), "utf-8"));

      db.exec("CREATE TABLE shop_vinext_schema(version INTEGER NOT NULL); INSERT INTO shop_vinext_schema VALUES (1)");
    } else if (!db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='shop_vinext_schema'").get()) {
      throw new Error("Refusing existing database without shop Vinext marker; use a new database path");
    }
    db.exec(fs.readFileSync(path.resolve(process.cwd(), "drizzle/shop_guards.sql"), "utf-8"));
    db.exec("COMMIT");
  } catch (error) {
    db.exec("ROLLBACK");
    db.close();
    throw error;
  }

  function wrapParam(param: any) {
    if (typeof param === "boolean") {
      return param ? 1 : 0;
    }
    if (param === undefined) {
      return null;
    }
    return param;
  }

  const d1 = {
    _rawDb: db,
    prepare(sqlQuery: string) {
      return {
        _sql: sqlQuery,
        _params: [] as any[],
        bind(...params: any[]) {
          this._params = params.map(wrapParam);
          return this;
        },
        async all<T = any>() {
          const stmt = db.prepare(this._sql);
          const results = stmt.all(...this._params) as T[];
          return { results, success: true, meta: { served_by: "sqlite-d1-shim" } };
        },
        async first<T = any>(colName?: string) {
          const stmt = db.prepare(this._sql);
          const row = stmt.get(...this._params) as any;
          if (!row) return null;
          if (colName) return (row[colName] ?? null) as T;
          return row as T;
        },
        async run() {
          const stmt = db.prepare(this._sql);
          const info = stmt.run(...this._params);
          return {
            success: true,
            meta: {
              changes: info.changes,
              last_row_id: Number(info.lastInsertRowid),
              served_by: "sqlite-d1-shim",
            },
          };
        },
        async raw<T = any[]>() {
          const stmt = db.prepare(this._sql);
          stmt.setReturnArrays(true);
          return stmt.all(...this._params) as T[];
        },
      };
    },
    async batch<T = any>(statements: any[]) {
      db.exec("BEGIN TRANSACTION;");
      try {
        const results: any[] = [];
        for (const s of statements) {
          // Execute synchronously inside the SQLite transaction: awaiting each
          // statement permits another request to start a nested transaction.
          const stmt = db.prepare(s._sql);
          stmt.setReturnArrays(true);
          const rows = stmt.all(...s._params);
          results.push({ results: rows, success: true, meta: { served_by: "sqlite-d1-shim" } });
        }
        db.exec("COMMIT;");
        return results as T;
      } catch (err) {
        db.exec("ROLLBACK;");
        throw err;
      }
    },
    async exec(sqlScript: string) {
      db.exec(sqlScript);
      return { count: 0, duration: 0 };
    },
  };

  return d1;
}

export function getLocalSqliteD1(): any {
  if (!defaultD1) {
    const dbPath = path.resolve(process.cwd(), ".data/shop-vinext.sqlite");
    defaultD1 = createSqliteD1(dbPath);
  }
  return defaultD1;
}

export default {
  createSqliteD1,
  getLocalSqliteD1,
};
