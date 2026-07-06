import { auth, signOut } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export default async function ForbiddenPage() {
  const session = await auth();

  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="text-xl">Access denied</CardTitle>
          <CardDescription>
            {session?.user?.email ? `${session.user.email} is ` : "You are "}
            not authorized to use the admin console. Ask an administrator to
            grant your account the <span className="font-medium">admin</span>{" "}
            role.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            action={async () => {
              "use server";
              await signOut({ redirectTo: "/login" });
            }}
          >
            <Button className="w-full" variant="outline" type="submit">
              Sign out
            </Button>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
