import "server-only";

import { cookies } from "next/headers";
import {
  AdminApiError,
  AdminClient,
  AdminShell,
  isCustomResourcePage,
  isDetail,
  isPaginated,
  type ActionResponse,
  type ActionType,
  type ResourceInfo,
} from "@rxtech-lab/admin-generator-next";
import type {
  AdminAppProps,
  ResolvedAdminConfig,
} from "@rxtech-lab/admin-generator-next/server";
import { redirect } from "next/navigation";

const SIDEBAR_COLLAPSED_COOKIE_KEY = "ag_sidebar_collapsed";

async function clientFor(config: ResolvedAdminConfig) {
  const token = config.getToken ? await config.getToken() : null;
  return new AdminClient({
    apiUrl: config.apiUrl,
    basePath: config.apiBasePath,
    token,
  });
}

function isUnauthorized(error: unknown): error is AdminApiError {
  return error instanceof AdminApiError && error.status === 401;
}

async function signOutToLogin(): Promise<never> {
  redirect("/admin/sign-out");
}

// Local wrapper around the package AdminApp. The upstream component renders API
// errors inline, but a 401 means the saved admin session can no longer call the
// Go API, so clear it and send the user through sign-in again.
export async function AdminAppWithAuthRedirect({
  config,
  actions,
  params,
  searchParams,
  title,
  headerActions,
}: AdminAppProps) {
  const { slug } = await params;
  const sp = (await searchParams) ?? {};
  const initialSidebarCollapsed =
    (await cookies()).get(SIDEBAR_COLLAPSED_COOKIE_KEY)?.value === "true";

  const client = await clientFor(config);
  let resources: ResourceInfo[] = [];
  let error: string | undefined;

  try {
    resources = await client.listResources();
  } catch (err) {
    if (isUnauthorized(err)) await signOutToLogin();
    error = err instanceof Error ? err.message : "Failed to load resources";
  }

  const resourceId = slug?.[0];
  const dynamicPath =
    slug && slug.length > 1 ? slug.slice(1).join("/") : undefined;
  const action = (typeof sp.action === "string" ? sp.action : "view") as ActionType;
  let initialView:
    | {
        resourceId: string;
        action: ActionType;
        dynamicPath?: string;
        schema: Awaited<ReturnType<AdminClient["getSchema"]>>;
        initialData?: Extract<ActionResponse, { items: unknown[] }>;
        initialDetail?: Extract<ActionResponse, { data: Record<string, unknown> }>;
      }
    | undefined;

  if (resourceId && !error) {
    try {
      const schema = await client.getSchema(resourceId, action, dynamicPath);
      let initialData;
      let initialDetail;
      if (action === "view" && !isCustomResourcePage(schema)) {
        const resp = await client.fetchAction(resourceId, "view", {
          dynamicPath,
        });
        if (isPaginated(resp)) initialData = resp;
        else if (isDetail(resp)) initialDetail = resp;
      }
      initialView = {
        resourceId,
        action,
        dynamicPath,
        schema,
        initialData,
        initialDetail,
      };
    } catch (err) {
      if (isUnauthorized(err)) await signOutToLogin();
      error = err instanceof Error ? err.message : "Failed to load resource";
    }
  }

  const plainActions = {
    listResources: actions.listResources,
    getSchema: actions.getSchema,
    fetchAction: actions.fetchAction,
    fetchUrl: actions.fetchUrl,
    submitAction: actions.submitAction,
  };

  return (
    <AdminShell
      basePath={config.basePath}
      resources={resources}
      activeResourceId={resourceId}
      initialView={initialView}
      actions={plainActions}
      error={error}
      title={title}
      headerActions={headerActions}
      initialSidebarCollapsed={initialSidebarCollapsed}
    />
  );
}
