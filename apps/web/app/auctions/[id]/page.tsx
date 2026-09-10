import { notFound } from "next/navigation";
import AuctionDetailLive from "@/components/AuctionDetailLive";
import { ApiError, api, type Auction } from "@/lib/api";

const gradients = [
  "from-indigo-500 to-violet-600",
  "from-emerald-500 to-teal-600",
  "from-amber-500 to-orange-600",
  "from-sky-500 to-blue-600",
  "from-rose-500 to-pink-600",
  "from-fuchsia-500 to-purple-600",
];

function gradientFor(id: string): string {
  let hash = 0;
  for (let i = 0; i < id.length; i++) {
    hash = (hash * 31 + id.charCodeAt(i)) >>> 0;
  }
  return gradients[hash % gradients.length];
}

export default async function AuctionDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;

  let auction: Auction;
  try {
    // Server-side fetch for the initial paint + SEO; the live projection takes
    // over on hydration (components/AuctionDetailLive). The fetch goes straight
    // to the API origin (the /api proxy middleware only applies to browsers).
    auction = await api.getAuction(id);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      notFound();
    }
    throw err;
  }

  return (
    <AuctionDetailLive auction={auction}>
      <div
        className={`flex h-64 items-center justify-center rounded-2xl bg-gradient-to-br ${gradientFor(auction.id)} px-8`}
      >
        <span className="text-center text-lg font-semibold text-white/90">
          {auction.title}
        </span>
      </div>

      <section className="rounded-2xl border border-zinc-200 bg-white p-6 shadow-sm">
        <h2 className="font-semibold text-zinc-900">Description</h2>
        {auction.description ? (
          <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-600">
            {auction.description}
          </p>
        ) : (
          <p className="mt-2 text-sm italic text-zinc-400">
            No description provided.
          </p>
        )}
      </section>
    </AuctionDetailLive>
  );
}
