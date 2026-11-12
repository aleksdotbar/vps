import { useSuspenseQuery } from "@tanstack/react-query";
import { Banner, Empty, LayerCard, Link, Select } from "@cloudflare/kumo";
import { WarningIcon } from "@phosphor-icons/react";
import { getRouteApi, useRouter } from "@tanstack/react-router";
import { useState } from "react";

import type { Repository } from "../../gen/ts/panel/v1/panel_pb";
import { integrationQuery, repositoriesQuery } from "./api";
import { ButtonLink, Page } from "./components";
import { ProjectForm } from "./project-form";

const route = getRouteApi("/_private/projects/new");

export function NewProjectRoute() {
  const search = route.useSearch();
  return <CreateProject repositoryId={search.repository} />;
}

export function CreateProject({ repositoryId }: { repositoryId: string }) {
  const integration = useSuspenseQuery(integrationQuery()).data;
  return (
    <Page
      title="New project"
      description="Connect a GitHub repository to deploy from."
      crumbs={[{ label: "Projects", href: "/projects" }]}
    >
      <div className="flex max-w-2xl flex-col gap-6">
        {integration.installed ? (
          <RepositoryPicker repositoryId={repositoryId} />
        ) : (
          <Empty
            title="Connect GitHub first"
            description="The panel needs access to your repositories."
            contents={
              <ButtonLink to="/settings" hash="github">
                Open settings
              </ButtonLink>
            }
          />
        )}
      </div>
    </Page>
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

  function choose(value: string | null) {
    if (
      locked ||
      !value ||
      !selected.some((entry) => String(entry.githubRepositoryId) === value)
    )
      return;
    void router.navigate({
      to: "/projects/new",
      search: { repository: value },
    });
  }

  if (data.page?.nextPageToken)
    return (
      <Empty
        title="Too many repositories"
        description="Limit the GitHub App to at most 100 repositories in Settings."
        contents={
          <ButtonLink to="/settings" hash="github">
            Open settings
          </ButtonLink>
        }
      />
    );
  if (!selected.length && !locked)
    return (
      <Empty
        title="No repositories available"
        description="Choose which repositories the panel may use."
        contents={
          <ButtonLink to="/settings/repositories">
            Choose repositories
          </ButtonLink>
        }
      />
    );

  return (
    <>
      {error && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="Couldn’t refresh repositories. The list may be out of date."
        />
      )}
      <LayerCard>
        <LayerCard.Secondary>1. Repository</LayerCard.Secondary>
        <LayerCard.Primary>
          <Select
            label="Repository"
            placeholder="Choose a repository…"
            value={repositoryId || null}
            onValueChange={choose}
            disabled={locked || !!error}
            items={selected.map((entry) => ({
              value: String(entry.githubRepositoryId),
              label: `${entry.owner}/${entry.name}`,
            }))}
            description={
              <>
                Missing one?{" "}
                <Link href="/settings/repositories">Choose repositories</Link>.
              </>
            }
          />
        </LayerCard.Primary>
      </LayerCard>
      {repositoryId && !repository && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="That repository is no longer available. Choose another one."
        />
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
