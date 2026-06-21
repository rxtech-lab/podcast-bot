import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Debate Bot Dashboard",
  description: "Author and generate panel discussions with the debate-bot engine.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body className="min-h-screen antialiased">{children}</body>
    </html>
  );
}
