import { ImageResponse } from "next/og";
import sharp from "sharp";
import {
  coverImageURL,
  type AlbumDetail,
  type CreatorProfile,
  type DiscussionMeta,
} from "@/app/lib/backend";

export const OG_SIZE = { width: 1200, height: 630 };
export const OG_CONTENT_TYPE = "image/png";

async function imageDataURL(url: string | null | undefined): Promise<string | null> {
  if (!url) return null;

  try {
    const res = await fetch(url, { cache: "no-store" });
    if (!res.ok) return null;
    const input = Buffer.from(await res.arrayBuffer());
    const png = await sharp(input)
      .resize(380, 380, { fit: "cover", position: "centre" })
      .png()
      .toBuffer();
    return `data:image/png;base64,${png.toString("base64")}`;
  } catch (err) {
    console.warn("OG artwork image failed", err);
    return null;
  }
}

type OGCard = {
  eyebrow: string;
  title: string;
  subtitle?: string;
  imageURL?: string;
  gradientStart?: string;
  gradientEnd?: string;
  circularArtwork?: boolean;
  fallbackArtwork?: string;
  fallbackArtworkFontSize?: number;
};

async function renderOGCard(card: OGCard): Promise<ImageResponse> {
  const artwork = await imageDataURL(card.imageURL);
  const gradient = `linear-gradient(135deg, ${card.gradientStart || "#14b8a6"}, ${card.gradientEnd || "#f59e0b"})`;

  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          background: "#0a0a0a",
          color: "#fff",
          padding: 64,
          alignItems: "center",
          gap: 56,
        }}
      >
        <div
          style={{
            width: 380,
            height: 380,
            borderRadius: card.circularArtwork ? 999 : 40,
            display: "flex",
            background: gradient,
            flexShrink: 0,
            overflow: "hidden",
            alignItems: "center",
            justifyContent: "center",
            fontSize: card.fallbackArtworkFontSize || 112,
            fontWeight: 700,
            color: "rgba(255,255,255,0.9)",
          }}
        >
          {artwork ? (
            // eslint-disable-next-line @next/next/no-img-element
            <img
              src={artwork}
              alt=""
              width={380}
              height={380}
              style={{ objectFit: "cover" }}
            />
          ) : (
            card.fallbackArtwork || "FM"
          )}
        </div>
        <div style={{ display: "flex", flexDirection: "column", gap: 20 }}>
          <div style={{ fontSize: 28, opacity: 0.6, letterSpacing: 2 }}>
            {card.eyebrow}
          </div>
          <div style={{ fontSize: 64, fontWeight: 700, lineHeight: 1.1, maxWidth: 640 }}>
            {card.title}
          </div>
          {card.subtitle ? (
            <div style={{ fontSize: 32, lineHeight: 1.25, maxWidth: 640, opacity: 0.7 }}>
              {card.subtitle}
            </div>
          ) : null}
        </div>
      </div>
    ),
    OG_SIZE
  );
}

// renderOGImage builds the shared social-preview image for a discussion: the
// cover art on the left and the title/creator on the right.
export async function renderOGImage(meta: DiscussionMeta | null): Promise<ImageResponse> {
  return renderOGCard({
    eyebrow: "PANELFM",
    title: meta?.title?.trim() || meta?.topic?.trim() || "Podcast",
    subtitle: meta?.creator?.display_name ? `by ${meta.creator.display_name}` : undefined,
    imageURL: coverImageURL(meta) ?? undefined,
    gradientStart: meta?.cover?.gradient_start,
    gradientEnd: meta?.cover?.gradient_end,
  });
}

export async function renderHomepageOGImage(): Promise<ImageResponse> {
  return renderOGCard({
    eyebrow: "PODCASTFM",
    title: "Turn every question into a conversation",
    subtitle: "AI multi-speaker podcasts, structured knowledge, shareable content.",
    fallbackArtwork: "FM",
  });
}

export async function renderMarketplaceOGImage(): Promise<ImageResponse> {
  return renderOGCard({
    eyebrow: "PODCASTFM",
    title: "Podcast Marketplace",
    subtitle: "Public AI-generated podcasts from the community.",
    fallbackArtwork: "FM",
  });
}

export async function renderAlbumOGImage(detail: AlbumDetail | null): Promise<ImageResponse> {
  const album = detail?.album;
  const count = album?.episode_count ?? 0;
  return renderOGCard({
    eyebrow: album?.kind ? `${album.kind.toUpperCase()} ALBUM` : "PANELFM ALBUM",
    title: album?.title?.trim() || "Album",
    subtitle: detail
      ? `${count} ${count === 1 ? "episode" : "episodes"} on PanelFM`
      : "Listen on PanelFM",
    imageURL: album?.cover?.image_url,
    gradientStart: album?.cover?.gradient_start,
    gradientEnd: album?.cover?.gradient_end,
    fallbackArtwork: "ALBUM",
    fallbackArtworkFontSize: 76,
  });
}

export async function renderCreatorOGImage(
  creator: CreatorProfile | null
): Promise<ImageResponse> {
  const followers = creator?.follower_count ?? 0;
  const username = creator?.username ? `@${creator.username} · ` : "";
  return renderOGCard({
    eyebrow: "PANELFM CREATOR",
    title: creator?.display_name?.trim() || "Creator",
    subtitle: creator
      ? `${username}${followers} ${followers === 1 ? "follower" : "followers"}`
      : "Public podcasts on PanelFM",
    imageURL: creator?.avatar_url,
    circularArtwork: true,
    fallbackArtwork: creator?.display_name?.trim().charAt(0).toUpperCase() || "?",
  });
}
