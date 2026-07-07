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
  const result = await redirectOnUnauthorized(await actions.getSchema(...args));
  // Opt-in debug: set ADMIN_DEBUG_SCHEMA=1 to dump the exact schema the RJSF
  // form receives (e.g. to verify conditional `dependencies` fields render).
  if (process.env.ADMIN_DEBUG_SCHEMA === "1" && result.ok) {
    console.log(
      `[admin schema] ${JSON.stringify(args)}\n` +
        JSON.stringify(result.data, null, 2),
    );
  }
  return result;
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
