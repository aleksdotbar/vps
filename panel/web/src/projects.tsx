import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { environmentsQuery, projectQuery, projectsQuery } from "./api";
import { Details, Detail, Empty, ObservedTime, PageTitle } from "./components";

export function Projects({ cursor }: { cursor: string }) {
  const { data, error, isFetching } = useSuspenseQuery(projectsQuery(cursor));
  return (
    <>
      <PageTitle
        title="Projects"
        description="Repositories and their environments."
      >
        <Link to="/projects/new" search={{ repository: "", workflows: "" }}>
          New project
        </Link>
      </PageTitle>
      {error && (
        <p role="alert">Refresh failed. Showing previously loaded projects.</p>
      )}
      <section aria-label="Projects" aria-busy={isFetching}>
        {data.projects.length ? (
          <ul className="resource-list">
            {data.projects.map((project) => (
              <li key={project.id}>
                <div>
                  <Link
                    className="resource-name"
                    to="/projects/$projectId"
                    params={{ projectId: project.id }}
                    search={{ cursor: "" }}
                  >
                    {project.name}
                  </Link>
                  <p>
                    {project.repository
                      ? `${project.repository.owner}/${project.repository.name}`
                      : "Repository unavailable"}
                  </p>
                </div>
                <span className="muted">
                  Created <ObservedTime value={project.createdAt} />
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="No projects yet">
            Create a project to connect its repository and build workflow.
          </Empty>
        )}
      </section>
      <nav className="pagination" aria-label="Project pages">
        {cursor && (
          <Link to="/projects" search={{ cursor: "" }}>
            Newest projects
          </Link>
        )}
        {data.page?.nextPageToken && (
          <Link to="/projects" search={{ cursor: data.page.nextPageToken }}>
            Older projects
          </Link>
        )}
      </nav>
    </>
  );
}

export function Project({ id, cursor }: { id: string; cursor: string }) {
  const project = useSuspenseQuery(projectQuery(id)).data.project;
  const environments = useSuspenseQuery(environmentsQuery(id, cursor));
  if (!project)
    return (
      <Empty title="Project unavailable">
        Return to projects and refresh the list.
      </Empty>
    );

  return (
    <>
      <Link className="back-link" to="/projects" search={{ cursor: "" }}>
        All projects
      </Link>
      <PageTitle
        title={project.name}
        description={
          project.repository
            ? `${project.repository.owner}/${project.repository.name}`
            : undefined
        }
      >
        <Link
          to="/projects/$projectId/environments/new"
          params={{ projectId: id }}
          search={{ preset: "staging", servers: "" }}
        >
          New environment
        </Link>
      </PageTitle>
      <Details>
        <Detail label="GitHub workflow">
          {project.githubWorkflowId.toString()}
        </Detail>
        <Detail label="Created">
          <ObservedTime value={project.createdAt} />
        </Detail>
      </Details>
      <section
        aria-labelledby="environments-title"
        aria-busy={environments.isFetching}
      >
        <h2 id="environments-title">Environments</h2>
        {environments.error && (
          <p role="alert">
            Refresh failed. Showing previously loaded environments.
          </p>
        )}
        {environments.data.environments.length ? (
          <ul className="resource-list">
            {environments.data.environments.map((environment) => (
              <li key={environment.id}>
                <div>
                  <Link
                    className="resource-name"
                    to="/environments/$environmentId"
                    params={{ environmentId: environment.id }}
                    search={{ services: "", releases: "" }}
                  >
                    {environment.settings?.name || environment.id}
                  </Link>
                  <p>Branch: {environment.settings?.branch || "Unknown"}</p>
                </div>
                <span className="status">
                  {environment.pendingChanges
                    ? "Changes pending"
                    : environment.currentReleaseId
                      ? "Applied"
                      : "Not deployed"}
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="No environments yet">
            Create staging or production settings, then add its runtime secrets.
          </Empty>
        )}
      </section>
      <nav className="pagination" aria-label="Environment pages">
        {cursor && (
          <Link
            to="/projects/$projectId"
            params={{ projectId: id }}
            search={{ cursor: "" }}
          >
            Newest environments
          </Link>
        )}
        {environments.data.page?.nextPageToken && (
          <Link
            to="/projects/$projectId"
            params={{ projectId: id }}
            search={{ cursor: environments.data.page.nextPageToken }}
          >
            Older environments
          </Link>
        )}
      </nav>
    </>
  );
}
