import type { Metadata } from "next";
import Link from "next/link";
import { listPublicStations } from "@/app/lib/backend";
import { Navbar } from "@/app/components/Navbar";
import { StationCard } from "@/app/components/StationCard";
import { marketplaceLink, ogImageLink } from "@/app/lib/config";

const PAGE_SIZE = 24;

export const metadata: Metadata = {
  title: "PodcastFM — Marketplace",
  description: "Browse public AI-generated podcasts from the community and press play.",
  alternates: { canonical: marketplaceLink() },
  openGraph: {
    title: "PodcastFM — Marketplace",
    description: "Browse public AI-generated podcasts from the community and press play.",
    url: marketplaceLink(),
    type: "website",
    images: [
      {
        url: ogImageLink({ screen: "marketplace" }),
        width: 1200,
        height: 630,
        alt: "PodcastFM podcast marketplace",
      },
    ],
  },
  twitter: {
    card: "summary_large_image",
    title: "PodcastFM — Marketplace",
    description: "Browse public AI-generated podcasts from the community and press play.",
    images: [ogImageLink({ screen: "marketplace" })],
  },
};

type Props = {
  searchParams: Promise<{ q?: string | string[]; page?: string | string[] }>;
};

function first(value: string | string[] | undefined): string {
  return (Array.isArray(value) ? value[0] : value) ?? "";
}

// Public marketplace: a server-rendered grid of published podcasts. Search and
// pagination round-trip through query params so the page stays
// anonymous-friendly and shareable; clicking a card opens the /p/{id} player,
// which streams the audio.
export default async function MarketplacePage({ searchParams }: Props) {
  const sp = await searchParams;
  const q = first(sp.q).trim();
  const page = Math.max(1, Number.parseInt(first(sp.page), 10) || 1);

  const stations = await listPublicStations(q, PAGE_SIZE, (page - 1) * PAGE_SIZE);
  const hasNext = (stations?.length ?? 0) === PAGE_SIZE;

  const pageLink = (p: number) => {
    const search = new URLSearchParams();
    if (q) search.set("q", q);
    if (p > 1) search.set("page", String(p));
    const qs = search.toString();
    return qs ? `/marketplace?${qs}` : "/marketplace";
  };

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
        <Navbar current="marketplace" redirectTo="/marketplace" />

        <header className="mt-10">
          <h1 className="text-3xl font-semibold leading-tight text-white">Marketplace</h1>
          <p className="mt-2 max-w-xl text-sm leading-6 text-stone-400">
            Public AI-generated podcasts from the community. Pick a station and press
            play.
          </p>
        </header>

        <form action="/marketplace" className="mt-8 flex max-w-md items-center gap-2">
          <input
            type="search"
            name="q"
            defaultValue={q}
            placeholder="Search podcasts…"
            aria-label="Search podcasts"
            className="w-full flex-1 rounded-full border border-white/10 bg-white/[0.06] px-5 py-2.5 text-sm text-stone-100 placeholder:text-stone-500 outline-none transition focus:border-teal-300/50 focus:bg-white/[0.08]"
          />
          <button
            type="submit"
            className="shrink-0 rounded-full bg-teal-300 px-5 py-2.5 text-sm font-semibold text-black transition hover:bg-teal-200"
          >
            Search
          </button>
          {q ? (
            <Link
              href="/marketplace"
              className="shrink-0 rounded-full border border-white/10 bg-white/[0.06] px-4 py-2.5 text-sm text-stone-300 transition hover:bg-white/[0.12]"
            >
              Clear
            </Link>
          ) : null}
        </form>

        {stations === null ? (
          <section className="mt-16 rounded-lg border border-white/10 bg-white/[0.06] p-8 text-center">
            <h2 className="text-lg font-semibold text-white">Marketplace unavailable</h2>
            <p className="mt-2 text-sm leading-6 text-stone-400">
              We couldn&apos;t reach the podcast catalog. Please try again in a moment.
            </p>
          </section>
        ) : stations.length === 0 ? (
          <section className="mt-16 rounded-lg border border-white/10 bg-white/[0.06] p-8 text-center">
            <h2 className="text-lg font-semibold text-white">
              {q ? "No matches" : "No podcasts yet"}
            </h2>
            <p className="mt-2 text-sm leading-6 text-stone-400">
              {q
                ? `Nothing in the marketplace matches “${q}”.`
                : "Published podcasts will show up here."}
            </p>
          </section>
        ) : (
          <ul className="mt-10 grid grid-cols-2 gap-4 sm:grid-cols-3 sm:gap-5 lg:grid-cols-4">
            {stations.map((station) => (
              <li key={station.id}>
                <StationCard station={station} />
              </li>
            ))}
          </ul>
        )}

        {(page > 1 || hasNext) && (
          <nav aria-label="Pagination" className="mt-10 flex items-center justify-center gap-3">
            {page > 1 ? (
              <Link
                href={pageLink(page - 1)}
                className="rounded-full border border-white/10 bg-white/[0.06] px-5 py-2 text-sm font-medium text-stone-200 transition hover:bg-white/[0.12]"
              >
                Previous
              </Link>
            ) : null}
            <span className="text-xs tabular-nums text-stone-500">page {page}</span>
            {hasNext ? (
              <Link
                href={pageLink(page + 1)}
                className="rounded-full border border-white/10 bg-white/[0.06] px-5 py-2 text-sm font-medium text-stone-200 transition hover:bg-white/[0.12]"
              >
                Next
              </Link>
            ) : null}
          </nav>
        )}
      </div>
    </main>
  );
}
