import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { auth } from "@/app/lib/auth";
import { getViewerDiscussion } from "@/app/lib/viewer";
import { ExpiredView } from "@/app/components/DiscussionLanding";
import { PodcastViewer } from "./PodcastViewer";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  return { title: "Podcast", robots: { index: false }, alternates: { canonical: `/p/${id}` } };
}

// View-only web player. Requires sign-in (rxlab-auth); a signed-in user can open
// their own (private) podcasts and any public one. No composer, only playback,
// synced captions, and the full transcript.
export default async function PodcastViewerPage({ params }: Props) {
  const { id } = await params;

  const session = await auth();
  if (!session?.user?.id) {
    redirect(`/login?next=${encodeURIComponent(`/p/${id}`)}`);
  }

  const discussion = await getViewerDiscussion(id, session.accessToken);
  if (!discussion) return <ExpiredView />;

  if (discussion.status !== "ready") {
    return (
      <main className="grid min-h-screen place-items-center bg-[#060807] px-6 py-16 text-center text-stone-50">
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

  return <PodcastViewer discussion={discussion} />;
}
