import handler from "vinext/server/fetch-handler";
import { runWithDatabase } from "../db/index";
import { runWithConnectorBinding } from "../lib/connector-context";
import type { ConnectorBinding } from "../lib/connector-contract.mjs";
import { runWithInfrastructure } from "../lib/infrastructure";
import { dispatchInfrastructureOutbox } from "../lib/infrastructure-outbox";
import { proxyPlatformControl } from "../lib/platform-control";

const worker = {
  async fetch(request: Request, env: Cloudflare.Env, ctx: ExecutionContext<{ CONNECTORS?: ConnectorBinding }>) {
    if (!env.DB) return new Response("Database binding unavailable", { status: 503 });
    // Refuse old or incorrectly bound D1 databases before any handler can write.
    try {
      const marker = await env.DB.prepare("SELECT version FROM shop_vinext_schema WHERE version = 1 LIMIT 1").first();
      if (!marker) return new Response("Database not initialized for this system", { status: 503 });
    } catch { return new Response("Database not initialized for this system", { status: 503 }); }
    if (new URL(request.url).pathname.startsWith("/api/platform/control/")) return proxyPlatformControl(request, env.PLATFORM_CONTROL_URL);
    // Service-only scheduler endpoint bypasses demonstration identity and seeding.
    if (new URL(request.url).pathname === "/api/platform/dispatch") {
      if (!env.INFRA_GATEWAY_TOKEN) return Response.json({ status: "not_configured" }, { status: 503 });
      if (request.headers.get("authorization") !== `Bearer ${env.INFRA_GATEWAY_TOKEN}`) return Response.json({ status: "unauthorized" }, { status: 401 });
      if (request.method !== "POST") return new Response(null, { status: 405, headers: { Allow: "POST" } });
      return runWithInfrastructure(env, () => runWithDatabase(env.DB, async () => Response.json(await dispatchInfrastructureOutbox(), { headers: { "Cache-Control": "no-store" } })));
    }
    let binding = ctx.props?.CONNECTORS;
    // Local preview emulates the same request-scoped capability. This branch and
    // the auxiliary service binding are absent from production builds.
    if (import.meta.env.DEV && !binding && env.CONNECTORS) {
      const preview = env.CONNECTORS;
      const expiresAt = Date.now() + 60_000;
      binding = {
        async getContext() {
          if (Date.now() >= expiresAt) return { status: "request_context_expired" };
          return preview.getContext?.() ?? { status: "binding_unavailable" };
        },
        async invoke(connectorId, actionName, args) {
          if (Date.now() >= expiresAt) {
            return { status: "request_context_expired", message: "This request has expired. Please try again." };
          }
          return preview.invoke(connectorId, actionName, args);
        },
      };
    }
    return runWithInfrastructure(env, () => runWithDatabase(env.DB, () => runWithConnectorBinding(binding, () => handler.fetch(request, env, ctx))));
  },
  async scheduled(_event: ScheduledController, env: Cloudflare.Env) {
    if (!env.DB) throw new Error("Database binding unavailable");
    const marker = await env.DB.prepare("SELECT version FROM shop_vinext_schema WHERE version = 1 LIMIT 1").first();
    if (!marker) throw new Error("Database not initialized for this system");
    try { await runWithInfrastructure(env, () => runWithDatabase(env.DB, dispatchInfrastructureOutbox)); }
    catch (error) { console.error("Infrastructure scheduled dispatch failed", error instanceof Error ? error.message : "unknown"); throw error; }
  },
};

export default worker;
