import type { Metadata } from "next";
import { getShare } from "@/app/lib/backend";
import { DiscussionLanding, ExpiredView } from "@/app/components/DiscussionLanding";
import { appleItunesMeta } from "@/app/lib/appbanner";
import { ogImageLink, shareLink } from "@/app/lib/config";

type Props = { params: Promise<{ token: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { token } = await params;
  const meta = await getShare(token);
  if (!meta) {
    return { title: "Link unavailable", robots: { index: false } };
  }
  const url = shareLink(token);
  const description = meta.topic || `Join the discussion: ${meta.title}`;
  const image = ogImageLink({ token });
  return {
    title: meta.title,
    description,
    openGraph: {
      title: meta.title,
      description,
      url,
      type: "website",
      images: [{ url: image, width: 1200, height: 630, alt: "Podcast" }],
    },
    twitter: { card: "summary_large_image", title: meta.title, description, images: [image] },
    other: appleItunesMeta(url),
  };
}

export default async function SharePage({ params }: Props) {
  const { token } = await params;
  const meta = await getShare(token);
  if (!meta) return <ExpiredView />;
  return <DiscussionLanding meta={meta} deepLink={shareLink(token)} />;
}
