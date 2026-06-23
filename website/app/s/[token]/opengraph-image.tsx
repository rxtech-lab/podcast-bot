import { getShare } from "@/app/lib/backend";
import { renderOGImage, OG_SIZE, OG_CONTENT_TYPE } from "@/app/lib/og";

export const size = OG_SIZE;
export const contentType = OG_CONTENT_TYPE;
export const alt = "Discussion";

export default async function Image({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params;
  const meta = await getShare(token);
  return renderOGImage(meta);
}
