import { AdminAppWithAuthRedirect } from "@/lib/admin-app";
import { adminConfig } from "@/lib/admin-config";
import { auth, signOut } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import * as actions from "../actions";

export const dynamic = "force-dynamic";

export default async function AdminPage(props: {
  params: Promise<{ slug?: string[] }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const session = await auth();
  return (
    <AdminAppWithAuthRedirect
      config={adminConfig}
      actions={actions}
      params={props.params}
      searchParams={props.searchParams}
      title="Debate Bot Admin"
      headerActions={
        <>
          {session?.user?.email ? (
            <span className="text-sm text-muted-foreground">
              {session.user.email}
            </span>
          ) : null}
          <form
            action={async () => {
              "use server";
              await signOut({ redirectTo: "/login" });
            }}
          >
            <Button variant="ghost" size="sm" type="submit">
              Sign out
            </Button>
          </form>
        </>
      }
    />
  );
}
