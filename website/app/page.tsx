import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";
import { Navbar } from "@/app/components/Navbar";
import { DownloadBadges } from "@/app/components/DownloadBadges";
import { ScreenshotStack } from "@/app/components/ScreenshotStack";
import { homeLink, ogImageLink } from "@/app/lib/config";

const TITLE = "PodcastFM — Turn every question into a conversation";
const DESCRIPTION =
  "AI-powered multi-speaker podcasts from your ideas, questions, documents, and recordings — with transcripts, summaries, mind maps, and decks you can reuse.";

export const metadata: Metadata = {
  title: TITLE,
  description: DESCRIPTION,
  alternates: { canonical: homeLink() },
  openGraph: {
    title: TITLE,
    description: DESCRIPTION,
    url: homeLink(),
    type: "website",
    images: [{ url: ogImageLink({}), width: 1200, height: 630, alt: "PodcastFM" }],
  },
  twitter: {
    card: "summary_large_image",
    title: TITLE,
    description: DESCRIPTION,
    images: [ogImageLink({})],
  },
};

const SCREENSHOTS = Array.from({ length: 9 }, (_, i) => `/screenshots/${i + 1}.webp`);

const TEMPLATES = [
  {
    name: "Research",
    copy: "Send AI speakers into an unfamiliar topic and come back with the lay of the land.",
  },
  {
    name: "Brainstorming",
    copy: "Generate ideas from multiple perspectives, then build on the ones that stick.",
  },
  {
    name: "Debate",
    copy: "Two hosts argue both sides so you don't have to pick one blind.",
  },
  {
    name: "Problem-solving",
    copy: "Break a hard problem down and stress-test candidate solutions out loud.",
  },
  {
    name: "Decision-making",
    copy: "Weigh trade-offs with speakers who challenge each other's assumptions.",
  },
  {
    name: "Panel discussion",
    copy: "A roundtable of viewpoints on your subject, moderated for you.",
  },
  {
    name: "Conversation",
    copy: "An open-ended talk you can steer, interrupt, and join at any time.",
  },
] as const;

const OUTPUTS = [
  { name: "Audio & video", copy: "Podcast audio or broadcast-style video.", format: "MP4" },
  { name: "Full transcript", copy: "Every word, ready to search and quote.", format: "TXT" },
  { name: "Executive summary", copy: "The whole session in a two-minute read.", format: "TL;DR" },
  { name: "Insights & action items", copy: "What matters and what to do next.", format: "NEXT" },
  { name: "Interactive mind map", copy: "The argument's structure at a glance.", format: "MAP" },
  { name: "Presentation deck", copy: "Slides built straight from the discussion.", format: "DECK" },
  { name: "Notion export", copy: "Structured content, delivered to your workspace.", format: "NOTION" },
] as const;

const TRANSCRIPT_MOCK = [
  { speaker: "Host", width: "w-4/5", tone: "bg-teal-300/30" },
  { speaker: "Analyst", width: "w-full", tone: "bg-white/15" },
  { speaker: "Skeptic", width: "w-3/5", tone: "bg-amber-400/25" },
  { speaker: "Host", width: "w-11/12", tone: "bg-teal-300/30" },
  { speaker: "You", width: "w-2/3", tone: "bg-white/25" },
] as const;

type Props = {
  searchParams: Promise<{ q?: string | string[]; page?: string | string[] }>;
};

// PodcastFM marketing landing page. The marketplace used to live at this URL,
// so old search/pagination deep links (/?q=…&page=…) are forwarded to
// /marketplace before rendering.
export default async function LandingPage({ searchParams }: Props) {
  const sp = await searchParams;
  if (sp.q !== undefined || sp.page !== undefined) {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(sp)) {
      for (const v of Array.isArray(value) ? value : [value]) {
        if (v !== undefined) search.append(key, v);
      }
    }
    redirect(`/marketplace?${search.toString()}`);
  }

  return (
    <main className="relative min-h-screen flex-1 overflow-x-clip bg-[#060807] text-stone-50">
      <div
        className="pointer-events-none fixed inset-0 opacity-70"
        style={{
          background:
            "radial-gradient(circle at 18% 12%, #14b8a633, transparent 28rem), radial-gradient(circle at 82% 16%, #f59e0b2e, transparent 26rem), linear-gradient(135deg, #060807 0%, #10130f 46%, #090b10 100%)",
        }}
      />

      <div className="relative mx-auto w-full max-w-7xl px-5 py-10 sm:px-8 lg:px-10">
        <Navbar current="home" />

        {/* Hero */}
        <section className="grid items-center gap-12 py-16 sm:py-20 lg:grid-cols-[1.15fr_0.85fr] lg:py-24">
          <div>
            <span className="inline-flex items-center gap-2 rounded-full border border-white/10 bg-white/[0.06] px-4 py-1.5 text-xs font-medium uppercase tracking-wide text-teal-200/90">
              <span className="h-1.5 w-1.5 rounded-full bg-teal-300" />
              AI podcast studio
            </span>
            <h1 className="mt-6 text-4xl font-semibold leading-tight text-white sm:text-5xl lg:text-6xl">
              Turn every question into a{" "}
              <span className="bg-gradient-to-r from-teal-300 to-amber-400 bg-clip-text text-transparent">
                conversation
              </span>
              .
            </h1>
            <p className="mt-6 max-w-xl text-base leading-7 text-stone-400 sm:text-lg sm:leading-8">
              PodcastFM turns your ideas, questions, documents, and recordings into
              engaging multi-speaker podcasts — and every conversation into structured
              knowledge worth sharing.
            </p>
            <div className="mt-8 flex flex-col gap-5">
              <DownloadBadges />
              <Link
                href="/marketplace"
                className="w-fit text-sm font-medium text-teal-300 transition hover:text-teal-200"
              >
                Browse the marketplace →
              </Link>
            </div>
          </div>
          <div className="flex justify-center lg:justify-end lg:pr-10">
            <ScreenshotStack images={SCREENSHOTS} />
          </div>
        </section>

        {/* Templates and outputs */}
        <section className="py-16 sm:py-20">
          <div className="grid items-end gap-6 lg:grid-cols-[1fr_0.72fr]">
            <div>
              <p className="text-xs font-semibold uppercase tracking-[0.2em] text-teal-200/75">
                From prompt to publish
              </p>
              <h2 className="mt-3 max-w-3xl text-3xl font-semibold leading-tight text-white sm:text-4xl lg:text-5xl">
                Shape the conversation. Keep everything it creates.
              </h2>
            </div>
            <p className="max-w-xl text-sm leading-7 text-stone-400 sm:text-base">
              Choose how your speakers should think together, then turn the result into
              polished, reusable work — all from the same session.
            </p>
          </div>

          <div className="relative mt-10 overflow-hidden rounded-[1.75rem] border border-white/10 bg-[#0c0f0d]/90 shadow-2xl shadow-black/30">
            <div
              aria-hidden="true"
              className="pointer-events-none absolute inset-0 opacity-70"
              style={{
                background:
                  "radial-gradient(circle at 8% 16%, #14b8a61f, transparent 24rem), radial-gradient(circle at 92% 12%, #f59e0b1a, transparent 25rem)",
              }}
            />

            <div className="relative hidden items-center border-b border-white/10 px-8 py-5 text-[11px] font-semibold uppercase tracking-[0.18em] text-stone-500 sm:flex">
              <span className="text-teal-200/80">01 · Pick a template</span>
              <span className="mx-5 h-px flex-1 bg-gradient-to-r from-teal-300/40 to-white/10" />
              <span className="text-stone-400">02 · Start the conversation</span>
              <span className="mx-5 h-px flex-1 bg-gradient-to-r from-white/10 to-amber-300/40" />
              <span className="text-amber-300/80">03 · Use every output</span>
            </div>

            <div className="relative grid lg:grid-cols-2">
              <div className="p-5 sm:p-8 lg:border-r lg:border-white/10 lg:p-10">
                <div className="flex items-center justify-between gap-4">
                  <p className="text-xs font-semibold uppercase tracking-[0.2em] text-teal-200/75">
                    Templates
                  </p>
                  <span className="rounded-full border border-teal-300/15 bg-teal-300/[0.07] px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-teal-100/70">
                    7 ways to begin
                  </span>
                </div>
                <h3 className="mt-4 max-w-lg text-2xl font-semibold leading-tight text-white sm:text-3xl">
                  Start with how you want to think.
                </h3>
                <p className="mt-3 max-w-lg text-sm leading-6 text-stone-400">
                  Each template gives your AI speakers a distinct role, pace, and point
                  of view. You can interrupt, redirect, and ask follow-ups at any time.
                </p>

                <ol className="mt-8 grid gap-3 sm:grid-cols-2">
                  {TEMPLATES.map((template, index) => (
                    <li
                      key={template.name}
                      className="group rounded-xl border border-white/[0.08] bg-white/[0.035] p-4 transition duration-200 hover:-translate-y-0.5 hover:border-teal-300/30 hover:bg-teal-300/[0.055] last:sm:col-span-2"
                    >
                      <div className="flex items-start gap-3">
                        <span className="mt-0.5 text-[10px] font-semibold tabular-nums text-teal-300/60">
                          {String(index + 1).padStart(2, "0")}
                        </span>
                        <div>
                          <h4 className="text-sm font-semibold text-stone-100 transition group-hover:text-teal-100">
                            {template.name}
                          </h4>
                          <p className="mt-1.5 text-xs leading-5 text-stone-500">
                            {template.copy}
                          </p>
                        </div>
                      </div>
                    </li>
                  ))}
                </ol>
              </div>

              <div className="border-t border-white/10 p-5 sm:p-8 lg:border-t-0 lg:p-10">
                <div className="flex items-center justify-between gap-4">
                  <p className="text-xs font-semibold uppercase tracking-[0.2em] text-amber-300/80">
                    Outputs
                  </p>
                  <span className="rounded-full border border-amber-300/15 bg-amber-300/[0.07] px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-amber-100/70">
                    Made automatically
                  </span>
                </div>
                <h3 className="mt-4 max-w-lg text-2xl font-semibold leading-tight text-white sm:text-3xl">
                  Leave with more than an episode.
                </h3>
                <p className="mt-3 max-w-lg text-sm leading-6 text-stone-400">
                  One discussion becomes a complete set of assets for listening,
                  reviewing, presenting, and sharing.
                </p>

                <div className="mt-8 overflow-hidden rounded-2xl border border-amber-300/15 bg-gradient-to-br from-amber-300/[0.12] via-white/[0.045] to-teal-300/[0.07] p-5 sm:p-6">
                  <div className="flex items-start justify-between gap-5">
                    <div>
                      <span className="text-[10px] font-semibold uppercase tracking-[0.18em] text-amber-200/70">
                        Ready to play
                      </span>
                      <h4 className="mt-2 text-lg font-semibold text-white">
                        {OUTPUTS[0].name}
                      </h4>
                      <p className="mt-1 text-xs leading-5 text-stone-400">
                        {OUTPUTS[0].copy}
                      </p>
                    </div>
                    <span className="flex h-11 w-11 shrink-0 items-center justify-center rounded-full bg-amber-300 text-sm text-black shadow-lg shadow-amber-950/30">
                      <span aria-hidden="true" className="ml-0.5">▶</span>
                      <span className="sr-only">Playable audio and video</span>
                    </span>
                  </div>
                  <div aria-hidden="true" className="mt-7 flex h-12 items-end gap-1">
                    {[36, 62, 44, 80, 54, 92, 68, 40, 76, 52, 86, 46, 66, 34, 58, 78, 48, 70].map(
                      (height, index) => (
                        <span
                          key={index}
                          className="min-w-0 flex-1 rounded-full bg-gradient-to-t from-teal-300/35 to-amber-200/80"
                          style={{ height: `${height}%` }}
                        />
                      ),
                    )}
                  </div>
                  <div className="mt-4 flex items-center justify-between text-[10px] font-medium uppercase tracking-wider text-stone-500">
                    <span>Full episode</span>
                    <span>Audio · Video</span>
                  </div>
                </div>

                <ul className="mt-3 grid gap-3 sm:grid-cols-2">
                  {OUTPUTS.slice(1).map((output) => (
                    <li
                      key={output.name}
                      className="rounded-xl border border-white/[0.08] bg-white/[0.035] p-4 transition duration-200 hover:-translate-y-0.5 hover:border-amber-300/30 hover:bg-amber-300/[0.045]"
                    >
                      <div className="flex items-start gap-3">
                        <span className="min-w-10 rounded-md border border-white/10 bg-black/20 px-1.5 py-1 text-center text-[8px] font-bold uppercase tracking-wider text-amber-200/65">
                          {output.format}
                        </span>
                        <div>
                          <h4 className="text-xs font-semibold leading-5 text-stone-100">
                            {output.name}
                          </h4>
                          <p className="mt-1 text-[11px] leading-4 text-stone-500">
                            {output.copy}
                          </p>
                        </div>
                      </div>
                    </li>
                  ))}
                </ul>
              </div>
            </div>
          </div>
        </section>

        {/* Recordings */}
        <section className="grid items-center gap-12 py-16 sm:py-20 lg:grid-cols-2">
          <div>
            <p className="text-xs font-semibold uppercase tracking-widest text-teal-200/75">
              Recordings
            </p>
            <h2 className="mt-2 text-3xl font-semibold leading-tight text-white sm:text-4xl">
              Bring your own recordings
            </h2>
            <p className="mt-4 max-w-xl text-sm leading-7 text-stone-400 sm:text-base">
              Drop in meetings, interviews, lectures, or existing podcasts. PodcastFM
              generates transcripts, summaries, and structured insights automatically —
              so you can review hours of audio in minutes, then keep discussing the
              content with AI.
            </p>
          </div>
          <div className="rounded-lg border border-white/10 bg-white/[0.06] p-6">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium uppercase tracking-wide text-stone-500">
                Transcript
              </span>
              <span className="rounded-full border border-white/10 bg-white/[0.06] px-3 py-1 text-[10px] font-medium uppercase tracking-wide text-teal-200/90">
                Auto-generated
              </span>
            </div>
            <ul className="mt-5 flex flex-col gap-4">
              {TRANSCRIPT_MOCK.map((row, i) => (
                <li key={i} className="flex items-center gap-3">
                  <span className="w-14 shrink-0 text-[11px] font-medium text-stone-500">
                    {row.speaker}
                  </span>
                  <span className={`h-2.5 rounded-full ${row.width} ${row.tone}`} />
                </li>
              ))}
            </ul>
            <div className="mt-6 flex flex-wrap gap-2">
              {["Summary", "Key insights", "Action items"].map((chip) => (
                <span
                  key={chip}
                  className="rounded-full border border-white/10 bg-white/[0.06] px-3 py-1 text-xs text-stone-300"
                >
                  {chip}
                </span>
              ))}
            </div>
          </div>
        </section>

        {/* Marketplace CTA */}
        <section className="py-16 sm:py-20">
          <div className="relative overflow-hidden rounded-2xl border border-white/10 bg-white/[0.06] p-10 text-center sm:p-14">
            <div
              className="pointer-events-none absolute inset-0 opacity-60"
              style={{
                background:
                  "radial-gradient(circle at 25% 0%, #14b8a626, transparent 22rem), radial-gradient(circle at 75% 100%, #f59e0b1f, transparent 22rem)",
              }}
            />
            <div className="relative">
              <h2 className="mx-auto max-w-2xl text-3xl font-semibold leading-tight text-white sm:text-4xl">
                Explore what others are making
              </h2>
              <p className="mx-auto mt-4 max-w-xl text-sm leading-7 text-stone-400 sm:text-base">
                Valuable discussions don&apos;t need to stay private. Publish your best
                sessions to the global marketplace — and discover podcasts from a
                worldwide community.
              </p>
              <Link
                href="/marketplace"
                className="mt-8 inline-block rounded-full bg-teal-300 px-7 py-3 text-sm font-semibold text-black transition hover:bg-teal-200"
              >
                Browse the marketplace
              </Link>
            </div>
          </div>
        </section>

        {/* Footer */}
        <footer className="mt-8 flex flex-wrap items-center gap-4 border-t border-white/10 py-8">
          <span className="flex-1 text-xs font-semibold uppercase text-teal-200/75">
            podcast fm
          </span>
          <nav className="flex items-center gap-5 text-sm text-stone-400">
            <Link href="/" className="transition hover:text-stone-200">
              Home
            </Link>
            <Link href="/marketplace" className="transition hover:text-stone-200">
              Marketplace
            </Link>
          </nav>
          <span className="w-full text-xs text-stone-600 sm:w-auto">© 2026 rxlab</span>
        </footer>
      </div>
    </main>
  );
}
