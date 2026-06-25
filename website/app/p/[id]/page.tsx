import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { auth } from "@/app/lib/auth";
import { getViewerDiscussion, getViewerDiscussionLookup } from "@/app/lib/viewer";
import { ExpiredView } from "@/app/components/DiscussionLanding";
import { AccountMenu } from "@/app/components/AccountMenu";
import { SignOutButton } from "@/app/components/SignOutButton";
import { SignInButton } from "@/app/components/SignInButton";
import { appleItunesMeta } from "@/app/lib/appbanner";
import { ogImageLink, podcastLink } from "@/app/lib/config";
import { PodcastViewer } from "./PodcastViewer";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const session = await auth();
  const discussion = await getViewerDiscussion(id, session?.accessToken);
  if (!discussion) {
    return {
      title: "Podcast unavailable",
      robots: { index: false },
      alternates: { canonical: podcastLink(id) },
    };
  }

  const url = podcastLink(id);
  const description = discussion.topic || `Listen to: ${discussion.title}`;
  const image = ogImageLink({ id });
  return {
    title: discussion.title,
    description,
    robots: { index: false },
    alternates: { canonical: url },
    openGraph: {
      title: discussion.title,
      description,
      url,
      type: "website",
      images: [{ url: image, width: 1200, height: 630, alt: "Podcast" }],
    },
    twitter: { card: "summary_large_image", title: discussion.title, description, images: [image] },
    other: appleItunesMeta(url),
  };
}

// View-only web player. A public, ready podcast is viewable by anyone — no
// sign-in required. Private podcasts still need the owner's rxlab-auth session,
// so an anonymous request for one is sent to login. A signed-in user can open
// their own (private) podcasts and any public one. No composer, only playback,
// synced captions, and the full transcript.
export default async function PodcastViewerPage({ params }: Props) {
  const { id } = await params;

  const session = await auth();

  // Resolve the discussion first — don't gate on a session. An anonymous request
  // reaches only the public market endpoint (service token), so it succeeds for
  // public podcasts and returns null for private ones; a signed-in request can
  // also see the user's own private podcasts.
  const sessionAuthFailed = session?.error === "RefreshTokenError";
  const signedIn = !!session?.user?.id && !!session?.accessToken && !sessionAuthFailed;
  // Trailing header control. Signed in → account dropdown with sign-out; viewing
  // a public podcast anonymously → a sign-in button. Both keep the viewer on this
  // page afterwards. The player aligns the dropdown panel left (it sits next to
  // the brand); the not-ready corner aligns it right.
  const accountLabel = session?.user?.name || session?.user?.email || "Account";
  const playerAction = signedIn ? (
    <AccountMenu label={accountLabel} align="left">
      <SignOutButton redirectTo={`/p/${id}`} />
    </AccountMenu>
  ) : (
    <SignInButton redirectTo={`/p/${id}`} />
  );
  const cornerAction = signedIn ? (
    <AccountMenu label={accountLabel} align="right">
      <SignOutButton redirectTo={`/p/${id}`} />
    </AccountMenu>
  ) : (
    <SignInButton redirectTo={`/p/${id}`} />
  );

  const lookup = await getViewerDiscussionLookup(id, signedIn ? session.accessToken : undefined);
  const discussion = lookup.discussion;
  if (!discussion) {
    // Couldn't see it anonymously, or the signed-in backend token was rejected:
    // send the visitor through login. A valid signed-in user who still can't see
    // it gets the unavailable state (not theirs, not public, or expired).
    if (!signedIn || lookup.authFailed || sessionAuthFailed) {
      redirect(`/login?next=${encodeURIComponent(`/p/${id}`)}`);
    }
    return <ExpiredView />;
  }

  if (discussion.status !== "ready") {
    return (
      <main className="relative grid min-h-screen place-items-center bg-[#060807] px-6 py-16 text-center text-stone-50">
        <div className="absolute right-5 top-5">{cornerAction}</div>
        <section className="w-full max-w-md rounded-lg border border-white/10 bg-white/[0.06] p-8 shadow-2xl shadow-black/30">
          <div className="mx-auto grid h-14 w-14 place-items-center rounded-full bg-teal-300 text-black">
            <span className="h-4 w-4 rounded-full border-2 border-black" aria-hidden="true" />
          </div>
          <h1 className="mt-5 text-2xl font-semibold leading-tight">{discussion.title}</h1>
          <p className="mt-3 text-sm leading-6 text-stone-400">
            This podcast isn&apos;t ready to play yet. Check back once it has finished
            generating.
          </p>
        </section>
      </main>
    );
  }

  return <PodcastViewer discussion={discussion} headerAction={playerAction} />;
}
