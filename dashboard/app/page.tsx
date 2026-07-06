import { redirect } from "next/navigation";

// Rendered on demand (not statically collected): an unconditional redirect at
// the top of a static page trips Turbopack's page-data collector in Next 16.
export const dynamic = "force-dynamic";

export default function Home() {
  redirect("/admin");
}
