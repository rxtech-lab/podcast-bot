"use client";

import { useTransition } from "react";
import { Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { deleteProject } from "@/lib/actions/projects";

export function DeleteProjectButton({ id }: { id: string }) {
  const [pending, start] = useTransition();
  return (
    <Button
      variant="ghost"
      size="icon"
      disabled={pending}
      onClick={() => {
        if (confirm("Delete this project?")) {
          start(() => deleteProject(id));
        }
      }}
      aria-label="Delete project"
    >
      <Trash2 className="h-4 w-4" />
    </Button>
  );
}
