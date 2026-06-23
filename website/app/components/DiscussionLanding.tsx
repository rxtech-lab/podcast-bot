import { coverImageURL, type DiscussionMeta } from "@/app/lib/backend";

// DiscussionLanding is the minimal landing body. The real experience lives in
// the iOS app / App Clip — this page exists to drive the App Clip card and OG
// preview, plus an explicit "Open in app" affordance and a graceful fallback.
export function DiscussionLanding({
  meta,
  deepLink,
}: {
  meta: DiscussionMeta;
  deepLink: string;
}) {
  const cover = coverImageURL(meta);
  const gradient = meta.cover?.gradient_start && meta.cover?.gradient_end
    ? `linear-gradient(135deg, ${meta.cover.gradient_start}, ${meta.cover.gradient_end})`
    : "linear-gradient(135deg, #6366f1, #ec4899)";

  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 px-6 py-16 text-center">
      <div
        className="h-56 w-56 overflow-hidden rounded-3xl shadow-2xl"
        style={{ background: gradient }}
      >
        {cover ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img src={cover} alt="" className="h-full w-full object-cover" />
        ) : null}
      </div>
      <div className="max-w-md space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">{meta.title}</h1>
        {meta.creator?.display_name ? (
          <p className="text-sm opacity-70">by {meta.creator.display_name}</p>
        ) : null}
      </div>
      <a
        href={deepLink}
        className="rounded-full bg-foreground px-8 py-3 text-base font-medium text-background"
      >
        Open in app
      </a>
      <p className="max-w-xs text-xs opacity-50">
        Tap to open this discussion in the app. Don&apos;t have it? The App Clip
        opens automatically on supported devices.
      </p>
    </main>
  );
}

export function ExpiredView() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-4 px-6 py-16 text-center">
      <div className="text-5xl">🔗</div>
      <h1 className="text-2xl font-semibold tracking-tight">Link unavailable</h1>
      <p className="max-w-sm text-sm opacity-60">
        This share link has expired or was removed by its owner. Ask for a fresh
        link to join the discussion.
      </p>
    </main>
  );
}
