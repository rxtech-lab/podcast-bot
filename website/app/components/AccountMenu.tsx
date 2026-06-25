"use client";

import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

// Account dropdown for signed-in viewers: a compact avatar trigger (the viewer's
// initial) that opens a small menu showing their name and the actions passed as
// `children` — so the sign-out server action stays server-rendered. This owns
// only the open/close interaction (outside-click + Escape to dismiss). The
// avatar (rather than the full name) keeps it from duplicating the creator badge.
export function AccountMenu({
  label,
  align = "right",
  children,
}: {
  label: string;
  // Which edge the panel aligns to — "left" when the trigger sits on the left of
  // a row, "right" (default) when it sits on the right — so it never overflows.
  align?: "left" | "right";
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const initial = label.trim().charAt(0).toUpperCase() || "?";

  useEffect(() => {
    if (!open) return;
    function onPointerDown(event: MouseEvent) {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        className="grid h-7 w-7 place-items-center rounded-full border border-white/10 bg-white/[0.08] text-xs font-semibold uppercase text-stone-200/90 transition hover:bg-white/[0.14]"
      >
        {initial}
      </button>
      {open ? (
        <div
          role="menu"
          // No onClick-to-close here: closing would unmount the child <form>
          // mid-click and the browser cancels the submit ("form is not
          // connected"). Items that act (like sign-out) navigate away on their
          // own; outside-click and Escape dismiss the rest.
          className={`absolute z-20 mt-2 min-w-[11rem] overflow-hidden rounded-lg border border-white/10 bg-[#10120f] p-1 shadow-2xl shadow-black/40 ${
            align === "left" ? "left-0" : "right-0"
          }`}
        >
          <div className="truncate px-3 py-2 text-xs text-stone-400">{label}</div>
          <div className="mx-1 mb-1 h-px bg-white/10" />
          {children}
        </div>
      ) : null}
    </div>
  );
}
