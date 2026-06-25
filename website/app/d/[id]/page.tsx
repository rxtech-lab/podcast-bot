import type { Metadata } from "next";
import { getPublicDiscussion } from "@/app/lib/backend";
import { DiscussionLanding, ExpiredView } from "@/app/components/DiscussionLanding";
import { appleItunesMeta } from "@/app/lib/appbanner";
import { ogImageLink, publicLink } from "@/app/lib/config";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const meta = await getPublicDiscussion(id);
  if (!meta) {
    return { title: "Discussion unavailable", robots: { index: false } };
  }
  const url = publicLink(id);
  const description = meta.topic || `Listen to: ${meta.title}`;
  const image = ogImageLink({ id });
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

export default async function PublicDiscussionPage({ params }: Props) {
  const { id } = await params;
  const meta = await getPublicDiscussion(id);
  if (!meta) return <ExpiredView />;
  return <DiscussionLanding meta={meta} deepLink={publicLink(id)} listenURL={`/p/${id}`} />;
}
