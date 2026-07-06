import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Debate Bot Admin",
  description: "Admin console for the debate-bot engine.",
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
