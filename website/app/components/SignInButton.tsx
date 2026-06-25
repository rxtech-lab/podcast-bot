import { signIn } from "@/app/lib/auth";

// Server-action sign-in control for anonymous viewers of a public podcast.
// Mirrors the login page's signIn form, but inline in the player header so a
// signed-out visitor can authenticate without leaving the page — `redirectTo`
// brings them right back here (e.g. to open their own private podcasts).
export function SignInButton({
  redirectTo = "/",
  className,
}: {
  redirectTo?: string;
  className?: string;
}) {
  return (
    <form
      action={async () => {
        "use server";
        await signIn("rxlab", { redirectTo });
      }}
    >
      <button
        type="submit"
        className={
          className ??
          "rounded-full border border-white/10 bg-white/[0.08] px-3 py-1 text-xs font-medium text-stone-200/90 transition hover:bg-white/[0.14]"
        }
      >
        Sign in
      </button>
    </form>
  );
}
