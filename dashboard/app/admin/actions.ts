"use server";

import { createAdminActions } from "@rxtech-lab/admin-generator-next/server";
import { adminConfig } from "@/lib/admin-config";

// Exporting async functions from a "use server" module makes them callable from
// the admin client components. createAdminActions returns exactly such a bag.
const actions = createAdminActions(adminConfig);

export const listResources = actions.listResources;
export const getSchema = actions.getSchema;
export const fetchAction = actions.fetchAction;
export const fetchUrl = actions.fetchUrl;
export const submitAction = actions.submitAction;
