import { ImageResponse } from "next/og";
import sharp from "sharp";
import { coverImageURL, type DiscussionMeta } from "@/app/lib/backend";

export const OG_SIZE = { width: 1200, height: 630 };
export const OG_CONTENT_TYPE = "image/png";

async function coverDataURL(meta: DiscussionMeta | null): Promise<string | null> {
  const url = coverImageURL(meta);
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
    console.warn("OG cover image failed", err);
    return null;
  }
}

// renderOGImage builds the shared social-preview image for a discussion: the
// cover art on the left and the title/creator on the right.
export async function renderOGImage(meta: DiscussionMeta | null): Promise<ImageResponse> {
  const title = meta?.title?.trim() || meta?.topic?.trim() || "Podcast";
  const creator = meta?.creator?.display_name ?? "";
  const cover = await coverDataURL(meta);
  const gradient =
    meta?.cover?.gradient_start && meta?.cover?.gradient_end
      ? `linear-gradient(135deg, ${meta.cover.gradient_start}, ${meta.cover.gradient_end})`
      : "linear-gradient(135deg, #6366f1, #ec4899)";

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
            borderRadius: 40,
            display: "flex",
            background: gradient,
            flexShrink: 0,
            overflow: "hidden",
          }}
        >
          {cover ? (
            // eslint-disable-next-line @next/next/no-img-element
            <img src={cover} alt="" width={380} height={380} style={{ objectFit: "cover" }} />
          ) : null}
        </div>
        <div style={{ display: "flex", flexDirection: "column", gap: 20 }}>
          <div style={{ fontSize: 28, opacity: 0.6, letterSpacing: 2 }}>PANELFM</div>
          <div style={{ fontSize: 64, fontWeight: 700, lineHeight: 1.1, maxWidth: 640 }}>
            {title}
          </div>
          {creator ? (
            <div style={{ fontSize: 32, opacity: 0.7 }}>{`by ${creator}`}</div>
          ) : null}
        </div>
      </div>
    ),
    OG_SIZE
  );
}
