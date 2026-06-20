"use client";

import { useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { setVideoConfig } from "@/lib/actions/projects";
import { submitGeneration } from "@/lib/actions/generation";
import { defaultVideoConfig, type VideoConfig } from "@/lib/schema/script-types";

const SUBTITLE_LANGUAGES = [
  { code: "zh-Hans", label: "Simplified Chinese" },
  { code: "zh-Hant", label: "Traditional Chinese" },
  { code: "en", label: "English" },
  { code: "ja", label: "Japanese" },
  { code: "ko", label: "Korean" },
  { code: "es", label: "Spanish" },
  { code: "fr", label: "French" },
  { code: "de", label: "German" },
];

export function VideoConfigClient({ projectId }: { projectId: string }) {
  const router = useRouter();
  const [cfg, setCfg] = useState<VideoConfig>(defaultVideoConfig);
  const [error, setError] = useState<string | null>(null);
  const [pending, start] = useTransition();

  const toggleLang = (code: string) =>
    setCfg((c) => ({
      ...c,
      subtitle_languages: c.subtitle_languages.includes(code)
        ? c.subtitle_languages.filter((l) => l !== code)
        : [...c.subtitle_languages, code],
    }));

  const onGenerate = () =>
    start(async () => {
      setError(null);
      try {
        await setVideoConfig(projectId, cfg);
        await submitGeneration(projectId, cfg);
        router.refresh();
      } catch (e) {
        setError(e instanceof Error ? e.message : "submit failed");
      }
    });

  return (
    <main className="mx-auto max-w-xl p-6">
      <Card>
        <CardHeader>
          <CardTitle>Video configuration</CardTitle>
        </CardHeader>
        <CardContent className="space-y-5">
          <div className="space-y-1">
            <Label>Resolution</Label>
            <select
              className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
              value={cfg.resolution}
              onChange={(e) =>
                setCfg((c) => ({
                  ...c,
                  resolution: e.target.value as VideoConfig["resolution"],
                }))
              }
            >
              <option value="1080p">1080p</option>
              <option value="720p">720p</option>
            </select>
          </div>

          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Subtitles</legend>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={cfg.soft_subs}
                onChange={(e) =>
                  setCfg((c) => ({ ...c, soft_subs: e.target.checked }))
                }
              />
              Soft subtitles (embedded track)
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={cfg.burn_subs}
                onChange={(e) =>
                  setCfg((c) => ({ ...c, burn_subs: e.target.checked }))
                }
              />
              Burn-in subtitles
            </label>
          </fieldset>

          {cfg.soft_subs ? (
            <div className="space-y-2">
              <Label>Translated subtitle tracks</Label>
              <div className="grid grid-cols-2 gap-1.5">
                {SUBTITLE_LANGUAGES.map((l) => (
                  <label key={l.code} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={cfg.subtitle_languages.includes(l.code)}
                      onChange={() => toggleLang(l.code)}
                    />
                    {l.label}
                  </label>
                ))}
              </div>
            </div>
          ) : null}

          {error ? <p className="text-sm text-destructive">{error}</p> : null}

          <Button onClick={onGenerate} disabled={pending} className="w-full">
            {pending ? "Submitting…" : "Start generation"}
          </Button>
        </CardContent>
      </Card>
    </main>
  );
}
