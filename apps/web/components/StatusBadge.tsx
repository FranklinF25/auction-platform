import type { AuctionStatus } from "@/lib/api";

const styles: Record<AuctionStatus, { label: string; className: string }> = {
  active: {
    label: "Active",
    className: "bg-emerald-100 text-emerald-700",
  },
  closed: {
    label: "Closed",
    className: "bg-zinc-200 text-zinc-700",
  },
  cancelled: {
    label: "Cancelled",
    className: "bg-rose-100 text-rose-700",
  },
};

export default function StatusBadge({ status }: { status: AuctionStatus }) {
  const style = styles[status];
  return (
    <span
      className={`shrink-0 rounded-full px-2.5 py-0.5 text-xs font-semibold ${style.className}`}
    >
      {style.label}
    </span>
  );
}
