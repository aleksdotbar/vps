import { Button } from "@cloudflare/kumo/components/button";
import { useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";

import type { Repository } from "../../gen/ts/panel/v1/panel_pb";
import { integrationQuery, repositoriesQuery } from "./api";
import { Empty, PageTitle } from "./components";
import { ProjectForm } from "./project-form";

const route = getRouteApi("/_private/projects/new");

export function NewProjectRoute() {
  const search = route.useSearch();
  return (
    <CreateProject repositoryId={search.repository} cursor={search.workflows} />
  );
}

export function CreateProject({
  repositoryId,
  cursor,
}: {
  repositoryId: string;
  cursor: string;
}) {
  const integration = useSuspenseQuery(integrationQuery()).data;
  return (
    <>
      <Link className="back-link" to="/projects" search={{ cursor: "" }}>
        All projects
      </Link>
      <PageTitle
        title="New project"
        description="Connect a repository and its build workflow."
      />
      {integration.installed ? (
        <RepositoryPicker repositoryId={repositoryId} cursor={cursor} />
      ) : (
        <Empty title="Connect GitHub first">
          Register and install your App in <Link to="/settings">Settings</Link>.
        </Empty>
      )}
    </>
  );
}

function RepositoryPicker({
  repositoryId,
  cursor,
}: {
  repositoryId: string;
  cursor: string;
}) {
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
        search: { repository: value, workflows: "" },
      });
  }

  if (data.page?.nextPageToken)
    return (
      <Empty title="Repository list incomplete">
        Reconnect the App with at most 100 repositories from{" "}
        <Link to="/settings">Settings</Link>.
      </Empty>
    );
  if (!selected.length && !locked)
    return (
      <Empty title="No repositories available">
        Choose <Link to="/settings/repositories">repository access</Link> or
        update the installation in <Link to="/settings">Settings</Link>.
      </Empty>
    );

  return (
    <>
      {error && (
        <p role="alert">
          Repository refresh failed. Previously loaded repository access may be
          out of date.
        </p>
      )}
      <form className="settings-form" onSubmit={choose}>
        <label htmlFor="project-repository">Repository</label>
        <select
          id="project-repository"
          name="repository"
          defaultValue={repositoryId}
          key={repositoryId}
          required
          disabled={locked || !!error}
        >
          <option value="">Choose a repository</option>
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
          Choose repository
        </Button>
        <p className="muted">
          Missing a repository? Check{" "}
          <Link to="/settings/repositories">panel repository access</Link> or
          update the App installation in <Link to="/settings">Settings</Link>.
        </p>
      </form>
      {repositoryId && !repository && (
        <p role="alert">
          This repository is no longer available to the panel. Choose another
          repository.
        </p>
      )}
      {repository && (
        <ProjectForm
          key={String(repository.githubRepositoryId)}
          repository={repository}
          cursor={cursor}
          onLockChange={(value) =>
            setCreationRepository(value ? repository : undefined)
          }
        />
      )}
    </>
  );
}
