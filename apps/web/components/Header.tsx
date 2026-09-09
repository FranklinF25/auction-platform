"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { api } from "@/lib/api";

export default function Header() {
  const queryClient = useQueryClient();
  const router = useRouter();

  // 401 for guests is an expected state, not a transient failure: no retries.
  const { data: me } = useQuery({
    queryKey: ["me"],
    queryFn: () => api.me(),
    retry: false,
  });

  const logout = useMutation({
    mutationFn: () => api.logout(),
    onSuccess: () => {
      queryClient.setQueryData(["me"], null);
      queryClient.clear();
      router.push("/");
    },
  });

  const signedIn = me != null;

  return (
    <header className="sticky top-0 z-10 border-b border-zinc-200 bg-white/80 backdrop-blur">
      <div className="mx-auto flex w-full max-w-6xl items-center justify-between gap-4 px-4 py-3 sm:px-6">
        <Link
          href="/"
          className="text-lg font-bold tracking-tight text-zinc-900"
        >
          Subasta
          <span className="ml-2 rounded-full bg-indigo-100 px-2 py-0.5 text-xs font-medium text-indigo-700">
            live auctions
          </span>
        </Link>

        <nav className="flex items-center gap-2 text-sm sm:gap-3">
          {signedIn ? (
            <>
              <Link
                href="/sell"
                className="rounded-lg bg-indigo-600 px-3 py-1.5 font-semibold text-white shadow-sm transition hover:bg-indigo-500"
              >
                Create auction
              </Link>
              <span
                data-testid="me-name"
                className="hidden max-w-40 truncate text-zinc-600 sm:inline"
                title={me.email}
              >
                {me.name}
              </span>
              <button
                type="button"
                onClick={() => logout.mutate()}
                disabled={logout.isPending}
                className="rounded-lg border border-zinc-300 px-3 py-1.5 font-medium text-zinc-700 transition hover:bg-zinc-100 disabled:opacity-50"
              >
                {logout.isPending ? "Signing out…" : "Sign out"}
              </button>
            </>
          ) : (
            <>
              <Link
                href="/login"
                className="rounded-lg px-3 py-1.5 font-medium text-zinc-700 transition hover:bg-zinc-100"
              >
                Sign in
              </Link>
              <Link
                href="/register"
                className="rounded-lg bg-indigo-600 px-3 py-1.5 font-semibold text-white shadow-sm transition hover:bg-indigo-500"
              >
                Register
              </Link>
            </>
          )}
        </nav>
      </div>
    </header>
  );
}
