import { signOut } from "@/app/lib/auth";

// Server-action sign-out control. Mirrors the login page's signIn form. Rendered
// only for signed-in viewers — anonymous visitors of public podcasts have no
// session to end. Styled as a full-width menu item by default (it lives inside
// the AccountMenu dropdown); pass `className` to override. After signing out the
// viewer returns to `redirectTo` (defaults home) — pass the current page to stay
// put, e.g. a public podcast that's still viewable signed-out.
export function SignOutButton({
  className,
  redirectTo = "/",
}: {
  className?: string;
  redirectTo?: string;
}) {
  return (
    <form
      action={async () => {
        "use server";
        await signOut({ redirectTo });
      }}
    >
      <button
        type="submit"
        role="menuitem"
        className={
          className ??
          "flex w-full items-center rounded-md px-3 py-2 text-left text-sm text-stone-200 transition hover:bg-white/[0.08]"
        }
      >
        Sign out
      </button>
    </form>
  );
}
