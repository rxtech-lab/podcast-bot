import type { Metadata } from "next";
import Link from "next/link";
import { getAlbum } from "@/app/lib/backend";
import { formatDuration } from "@/app/components/StationCard";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const detail = await getAlbum(id);
  return {
    title: detail ? `${detail.album.title} — PanelFM` : "Album — PanelFM",
    description: detail
      ? `${detail.album.episode_count} episode album on PanelFM.`
      : "Album on PanelFM.",
    robots: { index: false },
  };
}

// Public album page: shared cover + title, then the episode list. Each episode
// row opens the /p/{id} player.
export default async function AlbumPage({ params }: Props) {
  const { id } = await params;
  const detail = await getAlbum(id);

  return (
    <main className="relative min-h-screen flex-1 overflow-x-clip bg-[#060807] text-stone-50">
      <div
        className="pointer-events-none fixed inset-0 opacity-70"
        style={{
          background:
            "radial-gradient(circle at 18% 12%, #14b8a633, transparent 28rem), radial-gradient(circle at 82% 16%, #f59e0b2e, transparent 26rem), linear-gradient(135deg, #060807 0%, #10130f 46%, #090b10 100%)",
        }}
      />

      <div className="relative mx-auto w-full max-w-5xl px-5 py-10 sm:px-8 lg:px-10">
        <Link
          href="/"
          className="text-xs font-semibold uppercase text-teal-200/75 transition hover:text-teal-200"
        >
          podcast fm
        </Link>

        {!detail ? (
          <section className="mt-16 rounded-lg border border-white/10 bg-white/[0.06] p-8 text-center">
            <h1 className="text-lg font-semibold text-white">Album not found</h1>
            <p className="mt-2 text-sm leading-6 text-stone-400">
              This album doesn&apos;t exist or isn&apos;t visible.
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
            <AlbumHeader detail={detail} />

            <section className="mt-10">
              <h2 className="text-xs font-semibold uppercase text-stone-500">Episodes</h2>
              {detail.episodes.length === 0 ? (
                <p className="mt-4 rounded-lg border border-white/10 bg-white/[0.06] px-5 py-6 text-sm text-stone-400">
                  No public episodes in this album yet.
                </p>
              ) : (
                <ol className="mt-4 space-y-2">
                  {detail.episodes.map((episode, i) => (
                    <li key={episode.id}>
                      <Link
                        href={`/p/${episode.id}`}
                        className="group flex items-center gap-4 rounded-lg border border-white/[0.08] bg-white/[0.035] px-4 py-3.5 transition hover:border-teal-300/40 hover:bg-white/[0.06]"
                      >
                        <span className="w-6 shrink-0 text-right text-sm tabular-nums text-stone-500">
                          {i + 1}
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm font-medium text-stone-100">
                            {episode.title}
                          </span>
                          {episode.creator?.display_name ? (
                            <span className="mt-0.5 block truncate text-xs text-stone-400">
                              {episode.creator.display_name}
                            </span>
                          ) : null}
                        </span>
                        <span className="shrink-0 text-xs tabular-nums text-stone-500">
                          {formatDuration(episode.duration_seconds)}
                        </span>
                        <span
                          aria-hidden="true"
                          className="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-white/[0.08] opacity-70 transition group-hover:bg-teal-300 group-hover:opacity-100"
                        >
                          <span className="ml-0.5 h-0 w-0 border-y-[0.3rem] border-l-[0.5rem] border-y-transparent border-l-current text-stone-200 group-hover:text-black" />
                        </span>
                      </Link>
                    </li>
                  ))}
                </ol>
              )}
            </section>
          </>
        )}
      </div>
    </main>
  );
}

function AlbumHeader({ detail }: { detail: NonNullable<Awaited<ReturnType<typeof getAlbum>>> }) {
  const { album } = detail;
  const coverURL = album.cover?.image_url?.trim();
  const coverStart = album.cover?.gradient_start || "#14b8a6";
  const coverEnd = album.cover?.gradient_end || "#f59e0b";

  return (
    <header className="mt-6 flex flex-wrap items-end gap-6">
      <div
        className="h-40 w-40 shrink-0 overflow-hidden rounded-lg border border-white/10 shadow-2xl shadow-black/40"
        style={{
          background: coverURL
            ? `linear-gradient(145deg, ${coverStart}66, ${coverEnd}44)`
            : `linear-gradient(145deg, ${coverStart}, ${coverEnd})`,
        }}
      >
        {coverURL ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img
            src={coverURL}
            alt=""
            className="h-full w-full object-cover"
            draggable={false}
          />
        ) : null}
      </div>
      <div className="min-w-0">
        <p className="text-xs font-semibold uppercase text-stone-500">
          {album.kind ? `${album.kind} album` : "Album"}
        </p>
        <h1 className="mt-1 text-3xl font-semibold leading-tight text-white">{album.title}</h1>
        <p className="mt-1 text-sm text-stone-400">
          {album.episode_count} {album.episode_count === 1 ? "episode" : "episodes"}
        </p>
      </div>
    </header>
  );
}
