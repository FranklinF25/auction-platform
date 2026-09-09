import type { Metadata } from "next";
import Header from "@/components/Header";
import Providers from "@/components/Providers";
import "./globals.css";

export const metadata: Metadata = {
  title: {
    default: "Subasta — live auctions",
    template: "%s — Subasta",
  },
  description:
    "Timed English auctions with live bidding. A Go + Next.js portfolio build.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <Providers>
          <div className="flex min-h-screen flex-col">
            <Header />
            <main className="mx-auto w-full max-w-6xl flex-1 px-4 pb-16 pt-8 sm:px-6">
              {children}
            </main>
            <footer className="border-t border-zinc-200 bg-white py-6 text-center text-sm text-zinc-500">
              Subasta · a Go + Next.js portfolio build
            </footer>
          </div>
        </Providers>
      </body>
    </html>
  );
}
