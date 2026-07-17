import type { Metadata } from "next";
import Link from "next/link";
import { auth } from "@/app/lib/auth";
import { getCreator, listCreatorStations } from "@/app/lib/backend";
import { creatorIdFromSlug, decodeRouteParam } from "@/app/lib/creator";
import { StationCard } from "@/app/components/StationCard";

const PAGE_SIZE = 24;

type Props = {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ page?: string | string[] }>;
};

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const creator = await getCreator(creatorIdFromSlug(decodeRouteParam(id)));
  return {
    title: creator ? `${creator.display_name} — PanelFM` : "Creator — PanelFM",
    description: creator
      ? `Public podcasts by ${creator.display_name}.`
      : "Creator profile on PanelFM.",
    robots: { index: false },
  };
}

// Public creator profile: their avatar/name plus a grid of every public
// podcast they published. Reached by tapping a creator's name anywhere in the
// marketplace or on the podcast player. The URL carries the bare id (no
// "oauth:" prefix); creatorIdFromSlug restores the backend form, and legacy
// prefixed links still resolve.
export default async function CreatorPage({ params, searchParams }: Props) {
  const { id } = await params;
  const slug = decodeRouteParam(id);
  const creatorId = creatorIdFromSlug(slug);
  const sp = await searchParams;
  const rawPage = Array.isArray(sp.page) ? sp.page[0] : sp.page;
  const page = Math.max(1, Number.parseInt(rawPage ?? "", 10) || 1);

  // Prefer the signed-in user's token so creators can open their own page
  // (is_self) even before they have public podcasts; anonymous visitors go
  // through the service token.
  const session = await auth();
  const accessToken =
    session?.error === "RefreshTokenError" ? undefined : session?.accessToken;

  const [creator, stations] = await Promise.all([
    getCreator(creatorId, accessToken),
    listCreatorStations(creatorId, PAGE_SIZE, (page - 1) * PAGE_SIZE, accessToken),
  ]);
  const hasNext = (stations?.length ?? 0) === PAGE_SIZE;

  const base = `/c/${encodeURIComponent(slug)}`;
  const pageLink = (p: number) => (p > 1 ? `${base}?page=${p}` : base);

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
        <Link
          href="/"
          className="text-xs font-semibold uppercase text-teal-200/75 transition hover:text-teal-200"
        >
          podcast fm
        </Link>

        {!creator ? (
          <section className="mt-16 rounded-lg border border-white/10 bg-white/[0.06] p-8 text-center">
            <h1 className="text-lg font-semibold text-white">Creator not found</h1>
            <p className="mt-2 text-sm leading-6 text-stone-400">
              This creator doesn&apos;t exist or isn&apos;t visible.
            </p>
            <Link
              href="/"
              className="mt-5 inline-block rounded-full border border-white/10 bg-white/[0.08] px-5 py-2 text-sm font-medium text-stone-200 transition hover:bg-white/[0.14]"
            >
              Back to marketplace
            </Link>
          </section>
        ) : (
          <>
            <header className="mt-6 flex items-center gap-5">
              {creator.avatar_url ? (
                // eslint-disable-next-line @next/next/no-img-element
                <img
                  src={creator.avatar_url}
                  alt=""
                  className="h-20 w-20 shrink-0 rounded-full border border-white/10 object-cover"
                  draggable={false}
                />
              ) : (
                <div className="grid h-20 w-20 shrink-0 place-items-center rounded-full bg-white/[0.12] text-2xl font-semibold uppercase text-stone-200">
                  {creator.display_name.trim().charAt(0).toUpperCase() || "?"}
                </div>
              )}
              <div className="min-w-0">
                <p className="text-xs font-semibold uppercase text-stone-500">Creator</p>
                <h1 className="mt-1 truncate text-3xl font-semibold leading-tight text-white">
                  {creator.display_name}
                </h1>
                <p className="mt-1 text-sm text-stone-400">
                  {creator.username ? `@${creator.username} · ` : ""}
                  {creator.follower_count}{" "}
                  {creator.follower_count === 1 ? "follower" : "followers"}
                </p>
              </div>
            </header>

            {stations === null ? (
              <section className="mt-16 rounded-lg border border-white/10 bg-white/[0.06] p-8 text-center">
                <h2 className="text-lg font-semibold text-white">Podcasts unavailable</h2>
                <p className="mt-2 text-sm leading-6 text-stone-400">
                  We couldn&apos;t load this creator&apos;s podcasts. Please try again in a
                  moment.
                </p>
              </section>
            ) : stations.length === 0 ? (
              <section className="mt-16 rounded-lg border border-white/10 bg-white/[0.06] p-8 text-center">
                <h2 className="text-lg font-semibold text-white">No public podcasts</h2>
                <p className="mt-2 text-sm leading-6 text-stone-400">
                  {creator.display_name} hasn&apos;t published any podcasts yet.
                </p>
              </section>
            ) : (
              <ul className="mt-10 grid grid-cols-2 gap-4 sm:grid-cols-3 sm:gap-5 lg:grid-cols-4">
                {stations.map((station) => (
                  <li key={station.id}>
                    <StationCard station={station} showCreator={false} />
                  </li>
                ))}
              </ul>
            )}

            {(page > 1 || hasNext) && (
              <nav
                aria-label="Pagination"
                className="mt-10 flex items-center justify-center gap-3"
              >
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
          </>
        )}
      </div>
    </main>
  );
}
