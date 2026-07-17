import { getAlbum, getCreator, getPublicDiscussion, getShare } from "@/app/lib/backend";
import { creatorIdFromSlug, decodeRouteParam } from "@/app/lib/creator";
import {
  renderAlbumOGImage,
  renderCreatorOGImage,
  renderHomepageOGImage,
  renderMarketplaceOGImage,
  renderOGImage,
} from "@/app/lib/og";

export const dynamic = "force-dynamic";

export async function GET(request: Request) {
  const { searchParams } = new URL(request.url);
  const token = searchParams.get("token")?.trim();
  const id = searchParams.get("id")?.trim();
  const album = searchParams.get("album")?.trim();
  const creator = searchParams.get("creator")?.trim();
  const screen = searchParams.get("screen")?.trim();

  if (token) {
    return await renderOGImage(await getShare(token));
  }
  if (id) {
    return await renderOGImage(await getPublicDiscussion(id));
  }
  if (album) {
    return await renderAlbumOGImage(await getAlbum(album));
  }
  if (creator) {
    return await renderCreatorOGImage(
      await getCreator(creatorIdFromSlug(decodeRouteParam(creator)))
    );
  }
  if (screen === "marketplace") {
    return await renderMarketplaceOGImage();
  }
  return await renderHomepageOGImage();
}
