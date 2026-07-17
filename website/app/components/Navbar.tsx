import Link from "next/link";
import { auth } from "@/app/lib/auth";
import { AccountMenu } from "@/app/components/AccountMenu";
import { SignOutButton } from "@/app/components/SignOutButton";
import { SignInButton } from "@/app/components/SignInButton";

type Props = {
  current?: "home" | "marketplace";
  redirectTo?: string;
};

// Shared top navigation: wordmark, Home/Marketplace links, and the auth
// chrome. Server component — it resolves the session itself so pages don't
// have to thread it through.
export async function Navbar({ current, redirectTo = "/" }: Props) {
  const session = await auth();
  const signedIn =
    !!session?.user?.id && !!session?.accessToken && session.error !== "RefreshTokenError";
  const accountLabel = session?.user?.name || session?.user?.email || "Account";

  const linkClass = (active: boolean) =>
    active
      ? "rounded-full border border-white/10 bg-white/[0.06] px-4 py-1.5 text-sm font-medium text-white"
      : "rounded-full border border-transparent px-4 py-1.5 text-sm font-medium text-stone-400 transition hover:text-stone-200";

  return (
    <nav className="flex flex-wrap items-center gap-4">
      <Link
        href="/"
        className="text-xs font-semibold uppercase text-teal-200/75 transition hover:text-teal-200"
      >
        podcast fm
      </Link>
      <div className="flex flex-1 items-center gap-1">
        <Link href="/" className={linkClass(current === "home")}>
          Home
        </Link>
        <Link href="/marketplace" className={linkClass(current === "marketplace")}>
          Marketplace
        </Link>
      </div>
      {signedIn ? (
        <AccountMenu label={accountLabel} align="right">
          <SignOutButton redirectTo={redirectTo} />
        </AccountMenu>
      ) : (
        <SignInButton redirectTo={redirectTo} />
      )}
    </nav>
  );
}
