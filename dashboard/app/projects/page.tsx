import Link from "next/link";
import { listProjects } from "@/lib/actions/projects";
import { AppHeader } from "@/components/app-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { DeleteProjectButton } from "./project-list";

const STATUS_LABEL: Record<string, string> = {
  planning: "Planning",
  setup: "Editing",
  generating: "Generating",
  done: "Done",
};

export default async function ProjectsPage() {
  const projects = await listProjects();

  return (
    <div>
      <AppHeader />
      <main className="mx-auto max-w-5xl p-6">
        <div className="mb-6 flex items-center justify-between">
          <h1 className="text-2xl font-semibold">Your projects</h1>
          <Link href="/project/new">
            <Button>New project</Button>
          </Link>
        </div>

        {projects.length === 0 ? (
          <Card>
            <CardContent className="p-10 text-center text-muted-foreground">
              No projects yet. Create your first discussion script.
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {projects.map((p) => {
              const href =
                p.status === "planning"
                  ? "/project/new"
                  : p.status === "setup"
                    ? `/project/new/${p.id}`
                    : `/project/${p.id}`;
              return (
                <Card key={p.id} className="flex flex-col">
                  <CardHeader className="flex-1">
                    <div className="flex items-start justify-between gap-2">
                      <CardTitle className="text-base">{p.title}</CardTitle>
                      <Badge>{STATUS_LABEL[p.status] ?? p.status}</Badge>
                    </div>
                    {p.topic ? (
                      <p className="line-clamp-2 text-sm text-muted-foreground">
                        {p.topic}
                      </p>
                    ) : null}
                  </CardHeader>
                  <CardContent className="flex items-center gap-2">
                    <Link href={href} className="flex-1">
                      <Button variant="outline" size="sm" className="w-full">
                        Open
                      </Button>
                    </Link>
                    <DeleteProjectButton id={p.id} />
                  </CardContent>
                </Card>
              );
            })}
          </div>
        )}
      </main>
    </div>
  );
}
