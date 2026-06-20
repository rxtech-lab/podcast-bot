"use client";

import { useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { generateScript, reviseScript } from "@/lib/actions/planning";
import { createProject } from "@/lib/actions/projects";
import type { DiscussionScript } from "@/lib/schema/script-types";

export function PlanningClient() {
  const router = useRouter();
  const [topic, setTopic] = useState("");
  const [discussants, setDiscussants] = useState(3);
  const [draft, setDraft] = useState<DiscussionScript | null>(null);
  const [researched, setResearched] = useState(false);
  const [instruction, setInstruction] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, start] = useTransition();

  const onGenerate = () =>
    start(async () => {
      setError(null);
      try {
        const res = await generateScript({ topic, discussants });
        setDraft(res.script);
        setResearched(res.researched);
      } catch (e) {
        setError(e instanceof Error ? e.message : "generation failed");
      }
    });

  const onImprove = () =>
    start(async () => {
      if (!draft) return;
      setError(null);
      try {
        const res = await reviseScript({ previousScript: draft, instruction });
        setDraft(res.script);
        setInstruction("");
      } catch (e) {
        setError(e instanceof Error ? e.message : "revision failed");
      }
    });

  const onConfirm = () =>
    start(async () => {
      if (!draft) return;
      setError(null);
      try {
        const project = await createProject({
          title: draft.title,
          topic,
          scriptJson: draft,
        });
        router.push(`/project/new/${project.id}`);
      } catch (e) {
        setError(e instanceof Error ? e.message : "could not create project");
      }
    });

  return (
    <div className="space-y-5">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            Discussion <Badge className="ml-2">only type supported</Badge>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="space-y-1">
            <Label htmlFor="topic">Topic</Label>
            <Textarea
              id="topic"
              placeholder="What should the panel discuss?"
              value={topic}
              onChange={(e) => setTopic(e.target.value)}
            />
          </div>
          <div className="flex items-end gap-3">
            <div className="space-y-1">
              <Label htmlFor="n">Discussants</Label>
              <Input
                id="n"
                type="number"
                min={2}
                max={6}
                className="w-24"
                value={discussants}
                onChange={(e) => setDiscussants(Number(e.target.value))}
              />
            </div>
            <Button onClick={onGenerate} disabled={pending || !topic.trim()}>
              {pending && !draft ? "Drafting…" : "Generate script"}
            </Button>
          </div>
        </CardContent>
      </Card>

      {error ? (
        <p className="text-sm text-destructive">{error}</p>
      ) : null}

      {draft ? (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{draft.title}</CardTitle>
            {!researched ? (
              <p className="text-xs text-muted-foreground">
                Drafted from the model&apos;s own knowledge (live web research not
                enabled).
              </p>
            ) : null}
          </CardHeader>
          <CardContent className="space-y-4">
            <div>
              <div className="mb-1 text-sm font-medium">Panel</div>
              <ul className="space-y-1 text-sm">
                <li>
                  <span className="font-medium">Host:</span> {draft.host.name}
                </li>
                {draft.discussants.map((d, i) => (
                  <li key={i}>
                    <span className="font-medium">{d.name}</span>
                    {d.aspect ? (
                      <span className="text-muted-foreground"> — {d.aspect}</span>
                    ) : null}
                  </li>
                ))}
              </ul>
            </div>
            {draft.background ? (
              <div>
                <div className="mb-1 text-sm font-medium">Background</div>
                <p className="whitespace-pre-wrap text-sm text-muted-foreground">
                  {draft.background}
                </p>
              </div>
            ) : null}

            <div className="space-y-2 border-t border-border pt-3">
              <Label htmlFor="improve">Ask for changes</Label>
              <div className="flex gap-2">
                <Input
                  id="improve"
                  placeholder="e.g. add an economist, make it more technical"
                  value={instruction}
                  onChange={(e) => setInstruction(e.target.value)}
                />
                <Button
                  variant="outline"
                  onClick={onImprove}
                  disabled={pending || !instruction.trim()}
                >
                  Improve
                </Button>
              </div>
            </div>

            <Button onClick={onConfirm} disabled={pending} className="w-full">
              Confirm &amp; start editing
            </Button>
          </CardContent>
        </Card>
      ) : null}
    </div>
  );
}
