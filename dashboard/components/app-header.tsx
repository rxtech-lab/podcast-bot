import Link from "next/link";
import { auth, signOut } from "@/lib/auth";
import { Button } from "@/components/ui/button";

export async function AppHeader() {
  const session = await auth();
  return (
    <header className="flex items-center justify-between border-b border-border px-6 py-3">
      <Link href="/projects" className="font-semibold">
        Debate Bot
      </Link>
      <div className="flex items-center gap-3 text-sm text-muted-foreground">
        {session?.user?.email ? <span>{session.user.email}</span> : null}
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
      </div>
    </header>
  );
}
