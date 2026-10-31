import { dispatchCommerceOutbox } from "./commerce-outbox";

export async function dispatchInfrastructureOutbox() {
  const result = await dispatchCommerceOutbox();
  return { ...result, mesSynced: 0 };
}
