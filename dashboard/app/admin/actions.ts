"use server";

import { createAdminActions } from "@rxtech-lab/admin-generator-next/server";
import type { ActionResult } from "@rxtech-lab/admin-generator-next/server";
import { adminConfig } from "@/lib/admin-config";
import { signOut } from "@/lib/auth";

// Exporting async functions from a "use server" module makes them callable from
// the admin client components. createAdminActions returns exactly such a bag.
const actions = createAdminActions(adminConfig);

async function redirectOnUnauthorized<T>(
  result: ActionResult<T>,
): Promise<ActionResult<T>> {
  if (!result.ok && result.status === 401) {
    await signOut({ redirectTo: "/login" });
  }
  return result;
}

export async function listResources() {
  return redirectOnUnauthorized(await actions.listResources());
}

export async function getSchema(
  ...args: Parameters<typeof actions.getSchema>
) {
  return redirectOnUnauthorized(await actions.getSchema(...args));
}

export async function fetchAction(
  ...args: Parameters<typeof actions.fetchAction>
) {
  return redirectOnUnauthorized(await actions.fetchAction(...args));
}

export async function fetchUrl(...args: Parameters<typeof actions.fetchUrl>) {
  return redirectOnUnauthorized(await actions.fetchUrl(...args));
}

export async function submitAction(
  ...args: Parameters<typeof actions.submitAction>
) {
  return redirectOnUnauthorized(await actions.submitAction(...args));
}
