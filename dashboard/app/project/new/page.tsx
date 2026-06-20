import { AppHeader } from "@/components/app-header";
import { PlanningClient } from "./planning-client";

export default function NewProjectPage() {
  return (
    <div>
      <AppHeader />
      <main className="mx-auto max-w-2xl p-6">
        <h1 className="mb-1 text-2xl font-semibold">New discussion</h1>
        <p className="mb-6 text-sm text-muted-foreground">
          Pick a script type and a topic. The engine drafts a panel for you to
          review before you start editing.
        </p>
        <PlanningClient />
      </main>
    </div>
  );
}
