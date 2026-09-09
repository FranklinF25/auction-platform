import AuctionBrowser from "@/components/AuctionBrowser";

export default function HomePage() {
  return (
    <div className="flex flex-col gap-6">
      <section className="rounded-2xl border border-zinc-200 bg-gradient-to-br from-indigo-600 to-violet-700 px-6 py-10 text-white shadow-sm sm:px-10">
        <h1 className="text-3xl font-bold tracking-tight sm:text-4xl">
          Bid live. Win big.
        </h1>
        <p className="mt-2 max-w-xl text-indigo-100">
          Timed English auctions with real-time bidding. Browse what&apos;s on
          the block, or list something of your own.
        </p>
      </section>

      <AuctionBrowser />
    </div>
  );
}
