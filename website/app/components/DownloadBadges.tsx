import { APP_STORE_URL, TESTFLIGHT_URL } from "@/app/lib/config";

function TestFlightLogo({ className = "" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="8.25" stroke="currentColor" strokeWidth="1.7" />
      <circle cx="12" cy="12" r="3.15" stroke="currentColor" strokeWidth="1.7" />
      <path
        d="M12 8.85V4.5M15.15 13.85l3.77 2.18M8.85 13.85l-3.77 2.18"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
      />
      <circle cx="12" cy="4.5" r="1.1" fill="currentColor" />
      <circle cx="18.92" cy="16.03" r="1.1" fill="currentColor" />
      <circle cx="5.08" cy="16.03" r="1.1" fill="currentColor" />
    </svg>
  );
}

// App Store / TestFlight badges for the landing hero. Each renders only when
// its NEXT_PUBLIC_* URL is configured; nothing renders when neither is set.
export function DownloadBadges() {
  if (!APP_STORE_URL && !TESTFLIGHT_URL) return null;

  return (
    <div className="flex flex-wrap items-center gap-3">
      {APP_STORE_URL ? (
        <a
          href={APP_STORE_URL}
          target="_blank"
          rel="noreferrer"
          className="inline-block w-fit transition-opacity hover:opacity-85 active:scale-95"
        >
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src="/download-appstore.svg"
            alt="Download on the App Store"
            className="h-[52px] w-auto"
          />
        </a>
      ) : null}
      {TESTFLIGHT_URL ? (
        <a
          href={TESTFLIGHT_URL}
          target="_blank"
          rel="noreferrer"
          aria-label="Join the beta on TestFlight"
          className="group inline-flex h-[52px] w-[176px] items-center gap-3 rounded-[7px] border border-[#a6a6a6] bg-black px-3.5 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.18)] transition-[opacity,transform,box-shadow] hover:opacity-85 hover:shadow-[0_0_0_1px_rgba(255,255,255,0.16)] active:scale-95"
        >
          <span className="flex h-8 w-8 items-center justify-center rounded-full bg-[#0a84ff] text-white">
            <TestFlightLogo className="h-6 w-6" />
          </span>
          <span className="flex flex-col items-start leading-none">
            <span className="text-[9px] tracking-wide text-white/85">Join beta on</span>
            <span className="mt-1 font-sans text-[20px] font-semibold tracking-normal">
              TestFlight
            </span>
          </span>
        </a>
      ) : null}
    </div>
  );
}
