import { getPublicDiscussion, getShare } from "@/app/lib/backend";
import { renderOGImage } from "@/app/lib/og";

export const dynamic = "force-dynamic";

export async function GET(request: Request) {
  const { searchParams } = new URL(request.url);
  const token = searchParams.get("token")?.trim();
  const id = searchParams.get("id")?.trim();

  if (token) {
    return await renderOGImage(await getShare(token));
  }
  if (id) {
    return await renderOGImage(await getPublicDiscussion(id));
  }
  return await renderOGImage(null);
}
