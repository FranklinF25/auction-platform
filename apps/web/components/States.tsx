/** Shared loading / error / empty states so every view reads consistently. */

export function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-zinc-200 bg-white px-6 py-12 text-center shadow-sm">
      <p className="font-semibold text-zinc-900">Something went wrong</p>
      <p className="max-w-md text-sm text-zinc-500">{message}</p>
      {onRetry ? (
        <button
          type="button"
          onClick={onRetry}
          className="mt-1 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white shadow-sm transition hover:bg-indigo-500"
        >
          Try again
        </button>
      ) : null}
    </div>
  );
}

export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-zinc-300 bg-white/60 px-6 py-16 text-center">
      <p className="font-semibold text-zinc-900">{title}</p>
      {hint ? <p className="max-w-md text-sm text-zinc-500">{hint}</p> : null}
    </div>
  );
}

export function CardSkeleton() {
  return (
    <div className="overflow-hidden rounded-xl border border-zinc-200 bg-white shadow-sm">
      <div className="h-36 animate-pulse bg-zinc-200" />
      <div className="flex flex-col gap-3 p-4">
        <div className="h-4 w-3/4 animate-pulse rounded bg-zinc-200" />
        <div className="h-6 w-1/3 animate-pulse rounded bg-zinc-200" />
        <div className="h-3 w-1/2 animate-pulse rounded bg-zinc-100" />
      </div>
    </div>
  );
}

export function CardGridSkeleton({ count = 8 }: { count?: number }) {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
      {Array.from({ length: count }, (_, i) => (
        <CardSkeleton key={i} />
      ))}
    </div>
  );
}
