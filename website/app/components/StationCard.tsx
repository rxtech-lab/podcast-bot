import Link from "next/link";
import type { MarketStation } from "@/app/lib/backend";
import { creatorSlug } from "@/app/lib/creator";

// One marketplace grid card. The whole card opens the /p/{id} player via a
// stretched overlay link (z-10); the creator and album links sit above it
// (z-20) so they stay independently clickable without nesting anchors.
export function StationCard({
  station,
  showCreator = true,
}: {
  station: MarketStation;
  showCreator?: boolean;
}) {
  const coverURL = station.cover?.image_url?.trim();
  const coverStart = station.cover?.gradient_start || "#14b8a6";
  const coverEnd = station.cover?.gradient_end || "#f59e0b";

  return (
    <article className="group relative flex h-full flex-col overflow-hidden rounded-lg border border-white/[0.08] bg-white/[0.035] transition hover:border-teal-300/40 hover:bg-white/[0.06]">
      <Link
        href={`/p/${station.id}`}
        className="absolute inset-0 z-10"
        aria-label={`Play ${station.title}`}
      />
      <div
        className="relative aspect-square shrink-0 overflow-hidden"
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
            loading="lazy"
            className="h-full w-full object-cover transition duration-300 group-hover:scale-[1.03]"
            draggable={false}
          />
        ) : (
          <div className="flex h-full flex-col justify-between p-5">
            <div className="h-10 w-10 rounded-full bg-white/[0.24] blur-sm" />
            <div className="space-y-2">
              <div className="h-2 w-16 rounded-full bg-white/[0.35]" />
              <div className="h-2 w-24 rounded-full bg-white/[0.25]" />
            </div>
          </div>
        )}
        <div className="pointer-events-none absolute inset-0 grid place-items-center bg-black/40 opacity-0 transition group-hover:opacity-100">
          <span className="grid h-12 w-12 place-items-center rounded-full bg-teal-300 text-black shadow-lg shadow-teal-950/50">
            <span
              aria-hidden="true"
              className="ml-1 h-0 w-0 border-y-[0.4rem] border-l-[0.65rem] border-y-transparent border-l-black"
            />
          </span>
        </div>
      </div>
      <div className="flex flex-1 flex-col p-3.5">
        <h3 className="line-clamp-2 text-sm font-semibold leading-snug text-stone-100">
          {station.title}
        </h3>
        {showCreator && station.creator?.id && station.creator.display_name ? (
          <Link
            href={`/c/${encodeURIComponent(creatorSlug(station.creator.id))}`}
            className="relative z-20 mt-1 block w-fit max-w-full truncate text-xs text-stone-400 transition hover:text-teal-200"
          >
            {station.creator.display_name}
          </Link>
        ) : null}
        {station.album?.id ? (
          <Link
            href={`/a/${station.album.id}`}
            className="relative z-20 mt-2 inline-flex max-w-full items-center gap-1.5 rounded-full border border-white/10 bg-white/[0.06] px-2.5 py-0.5 text-[0.68rem] text-stone-300 transition hover:border-teal-300/40 hover:text-teal-200"
          >
            <span className="text-teal-200/80" aria-hidden="true">
              ▤
            </span>
            <span className="truncate">{station.album.title}</span>
          </Link>
        ) : null}
        <p className="mt-auto flex items-center justify-between pt-2 text-xs tabular-nums text-stone-500">
          <span>{formatDuration(station.duration_seconds)}</span>
          <span aria-label={`${station.like_count} likes`}>♥ {station.like_count}</span>
        </p>
      </div>
    </article>
  );
}

export function formatDuration(seconds: number | undefined): string {
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds <= 0) return "—";
  const total = Math.floor(seconds);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}
