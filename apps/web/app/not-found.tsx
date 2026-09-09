import Link from "next/link";

export default function NotFound() {
  return (
    <div className="flex flex-col items-center gap-3 py-24 text-center">
      <h1 className="text-3xl font-bold text-zinc-900">
        404 — nothing on the block here
      </h1>
      <p className="text-zinc-500">
        This page or auction doesn&apos;t exist.
      </p>
      <Link
        href="/"
        className="mt-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white shadow-sm transition hover:bg-indigo-500"
      >
        Back to auctions
      </Link>
    </div>
  );
}
