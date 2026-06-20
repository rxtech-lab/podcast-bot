import { redirect } from "next/navigation";
import { auth, signIn } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export default async function LoginPage() {
  const session = await auth();
  if (session?.user?.id) redirect("/projects");

  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="text-xl">Debate Bot Dashboard</CardTitle>
          <CardDescription>
            Sign in with your RxLab account to author and generate discussions.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            action={async () => {
              "use server";
              await signIn("rxlab", { redirectTo: "/projects" });
            }}
          >
            <Button className="w-full" type="submit">
              Sign in with RxLab
            </Button>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
