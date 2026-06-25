import type { Metadata } from "next";
import { getPublicDiscussion } from "@/app/lib/backend";
import { DiscussionLanding, ExpiredView } from "@/app/components/DiscussionLanding";
import { appleItunesMeta } from "@/app/lib/appbanner";
import { publicLink } from "@/app/lib/config";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const meta = await getPublicDiscussion(id);
  if (!meta) {
    return { title: "Discussion unavailable", robots: { index: false } };
  }
  const url = publicLink(id);
  return {
    title: meta.title,
    description: meta.topic || `Listen to: ${meta.title}`,
    openGraph: { title: meta.title, description: meta.topic, url, type: "website" },
    twitter: { card: "summary_large_image", title: meta.title, description: meta.topic },
    other: appleItunesMeta(url),
  };
}

export default async function PublicDiscussionPage({ params }: Props) {
  const { id } = await params;
  const meta = await getPublicDiscussion(id);
  if (!meta) return <ExpiredView />;
  return <DiscussionLanding meta={meta} deepLink={publicLink(id)} listenURL={`/p/${id}`} />;
}
