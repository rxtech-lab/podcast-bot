"use client";

import { useEffect, useRef, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";

const VISIBLE = 4;
const ADVANCE_MS = 3500;

// A fanned deck of app screenshots that auto-advances: the front card flies
// off while the rest spring forward. Hover pauses; the front card can also be
// dragged away. Falls back to a single static screenshot when the user prefers
// reduced motion.
export function ScreenshotStack({ images }: { images: string[] }) {
  const [index, setIndex] = useState(0);
  const hovered = useRef(false);
  const reducedMotion = useReducedMotion();

  useEffect(() => {
    if (reducedMotion || images.length < 2) return;
    const timer = setInterval(() => {
      if (!hovered.current) setIndex((i) => (i + 1) % images.length);
    }, ADVANCE_MS);
    return () => clearInterval(timer);
  }, [reducedMotion, images.length]);

  const cardChrome =
    "overflow-hidden rounded-[2rem] border border-white/10 bg-white/[0.06] shadow-2xl shadow-black/50";

  if (reducedMotion || images.length === 0) {
    return (
      <div className="relative w-[240px] sm:w-[280px] aspect-[1320/2868]">
        {images.length > 0 ? (
          <div className={`absolute inset-0 ${cardChrome}`}>
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src={images[0]}
              alt="PodcastFM app screenshot"
              className="h-full w-full object-cover"
              draggable={false}
            />
          </div>
        ) : null}
      </div>
    );
  }

  const cards = Array.from({ length: Math.min(VISIBLE, images.length) }, (_, pos) => ({
    src: images[(index + pos) % images.length],
    pos,
  }));

  return (
    <div
      className="relative w-[240px] sm:w-[280px] aspect-[1320/2868]"
      onMouseEnter={() => (hovered.current = true)}
      onMouseLeave={() => (hovered.current = false)}
    >
      <AnimatePresence initial={false}>
        {cards.map(({ src, pos }) => (
          <motion.div
            key={src}
            className={`absolute inset-0 ${cardChrome} ${pos === 0 ? "cursor-grab active:cursor-grabbing" : ""}`}
            style={{ zIndex: VISIBLE - pos }}
            initial={{ x: VISIBLE * 18, y: -(VISIBLE - 1) * 12, scale: 0.8, rotate: VISIBLE * 2.5, opacity: 0 }}
            animate={{
              x: pos * 18,
              y: pos * -12,
              scale: 1 - pos * 0.05,
              rotate: pos * 2.5,
              opacity: pos === VISIBLE - 1 ? 0.4 : 1 - pos * 0.12,
            }}
            exit={{ x: -140, y: 20, rotate: -8, opacity: 0 }}
            transition={{ type: "spring", stiffness: 260, damping: 28 }}
            drag={pos === 0 ? "x" : false}
            dragConstraints={{ left: 0, right: 0 }}
            dragElastic={0.6}
            onDragEnd={(_, info) => {
              if (Math.abs(info.offset.x) > 80) {
                setIndex((i) => (i + 1) % images.length);
              }
            }}
          >
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src={src}
              alt="PodcastFM app screenshot"
              className="pointer-events-none h-full w-full select-none object-cover"
              draggable={false}
              fetchPriority={pos === 0 ? "high" : undefined}
            />
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  );
}
