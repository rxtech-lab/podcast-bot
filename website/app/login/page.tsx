import { auth, signIn } from "@/app/lib/auth";

type Props = { searchParams: Promise<{ next?: string }> };

// Sign-in page for the view-only podcast viewer. Uses a server action to start
// the rxlab OIDC flow, returning to the page the user was trying to reach.
export default async function LoginPage({ searchParams }: Props) {
  const { next } = await searchParams;
  const hasExplicitNext = next && next.startsWith("/");
  const redirectTo = hasExplicitNext ? next : "/";
  const session = await auth();
  if (session?.user?.id && !hasExplicitNext) {
    // Already signed in and no protected page explicitly requested login.
    const { redirect } = await import("next/navigation");
    redirect(redirectTo);
  }

  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 px-6 py-16 text-center">
      <div className="max-w-sm space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">Sign in to listen</h1>
        <p className="text-sm opacity-60">
          Use your RxLab account to play this podcast with live captions and the
          full transcript.
        </p>
      </div>
      <form
        action={async () => {
          "use server";
          await signIn("rxlab", { redirectTo });
        }}
      >
        <button
          type="submit"
          className="rounded-full bg-foreground px-8 py-3 text-base font-medium text-background"
        >
          Sign in with RxLab
        </button>
      </form>
    </main>
  );
}
