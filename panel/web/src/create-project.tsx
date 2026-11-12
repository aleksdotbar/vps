import { useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";

import type { Repository } from "../../gen/ts/panel/v1/panel_pb";
import { integrationQuery, repositoriesQuery } from "./api";
import { Banner, Button, Card, Empty, PageHeader } from "./components";
import { ProjectForm } from "./project-form";

const route = getRouteApi("/_private/projects/new");

export function NewProjectRoute() {
  const search = route.useSearch();
  return <CreateProject repositoryId={search.repository} />;
}

export function CreateProject({ repositoryId }: { repositoryId: string }) {
  const integration = useSuspenseQuery(integrationQuery()).data;
  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link key="projects" to="/projects" search={{ cursor: "" }}>
            Projects
          </Link>,
        ]}
        title="New project"
        description="Connect a GitHub repository to deploy from."
      />
      <div className="stack" style={{ maxWidth: "40rem" }}>
        {integration.installed ? (
          <RepositoryPicker repositoryId={repositoryId} />
        ) : (
          <Card>
            <Empty
              title="Connect GitHub first"
              actions={
                <Link to="/settings" hash="github">
                  Open settings
                </Link>
              }
            >
              The panel needs access to your repositories.
            </Empty>
          </Card>
        )}
      </div>
    </>
  );
}

function RepositoryPicker({ repositoryId }: { repositoryId: string }) {
  const router = useRouter();
  const { data, error } = useSuspenseQuery(repositoriesQuery());
  const [creationRepository, setCreationRepository] = useState<Repository>();
  const locked = !!creationRepository;
  const selected = data.repositories.filter((repository) =>
    data.selectedRepositoryIds.includes(repository.githubRepositoryId),
  );
  const repository =
    creationRepository ??
    selected.find((value) => String(value.githubRepositoryId) === repositoryId);

  function choose(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (locked) return;
    const value = new FormData(event.currentTarget).get("repository");
    if (
      typeof value === "string" &&
      selected.some((entry) => String(entry.githubRepositoryId) === value)
    )
      void router.navigate({
        to: "/projects/new",
        search: { repository: value },
      });
  }

  if (data.page?.nextPageToken)
    return (
      <Card>
        <Empty title="Too many repositories">
          Limit the GitHub App to at most 100 repositories in{" "}
          <Link to="/settings" hash="github">
            Settings
          </Link>
          .
        </Empty>
      </Card>
    );
  if (!selected.length && !locked)
    return (
      <Card>
        <Empty
          title="No repositories available"
          actions={<Link to="/settings/repositories">Choose repositories</Link>}
        >
          Choose which repositories the panel may use.
        </Empty>
      </Card>
    );

  return (
    <>
      {error && (
        <Banner tone="warning">
          Couldn’t refresh repositories. The list may be out of date.
        </Banner>
      )}
      <Card title="1. Repository" labelledBy="repository-step-title">
        <form className="form" onSubmit={choose}>
          <div className="field">
            <label htmlFor="project-repository">Repository</label>
            <div className="row" style={{ flexWrap: "nowrap" }}>
              <select
                id="project-repository"
                name="repository"
                defaultValue={repositoryId}
                key={repositoryId}
                required
                disabled={locked || !!error}
              >
                <option value="">Choose a repository…</option>
                {selected.map((entry) => (
                  <option
                    key={String(entry.githubRepositoryId)}
                    value={String(entry.githubRepositoryId)}
                  >
                    {entry.owner}/{entry.name}
                  </option>
                ))}
              </select>
              <Button type="submit" disabled={locked || !!error}>
                Continue
              </Button>
            </div>
            <p className="help">
              Missing one?{" "}
              <Link to="/settings/repositories">Choose repositories</Link>.
            </p>
          </div>
        </form>
      </Card>
      {repositoryId && !repository && (
        <Banner tone="warning">
          That repository is no longer available. Choose another one.
        </Banner>
      )}
      {repository && (
        <ProjectForm
          key={String(repository.githubRepositoryId)}
          repository={repository}
          onLockChange={(value) =>
            setCreationRepository(value ? repository : undefined)
          }
        />
      )}
    </>
  );
}
