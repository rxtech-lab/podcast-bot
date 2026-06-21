import { notFound, redirect } from "next/navigation";
import { getProject } from "@/lib/actions/projects";

// Resume entry point — route to the right view for the project's state.
export default async function ProjectPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  const project = await getProject(id);
  if (!project) notFound();

  switch (project.status) {
    case "planning":
      redirect("/project/new");
    case "setup":
      redirect(`/project/new/${id}`);
    case "generating":
    case "done":
      redirect(`/project/${id}/generate`);
    default:
      redirect("/projects");
  }
}
