import { useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useRouter } from "@tanstack/react-router";
import { TriangleAlertIcon } from "lucide-react";
import { useState } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

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
      // Crumb links take untyped search; the route defaults cursor to "".
      crumbs={[{ label: "Projects", link: { to: "/projects" } }]}
    >
      <div className="flex max-w-2xl flex-col gap-6">
        {integration.installed ? (
          <RepositoryPicker repositoryId={repositoryId} />
        ) : (
          <Empty className="border">
            <EmptyHeader>
              <EmptyTitle>Connect GitHub first</EmptyTitle>
              <EmptyDescription>
                The panel needs access to your repositories.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <ButtonLink to="/settings" hash="github">
                Open settings
              </ButtonLink>
            </EmptyContent>
          </Empty>
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
  const items = selected.map((entry) => ({
    value: String(entry.githubRepositoryId),
    label: `${entry.owner}/${entry.name}`,
  }));

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
      <Empty className="border">
        <EmptyHeader>
          <EmptyTitle>Too many repositories</EmptyTitle>
          <EmptyDescription>
            Limit the GitHub App to at most 100 repositories in Settings.
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <ButtonLink to="/settings" hash="github">
            Open settings
          </ButtonLink>
        </EmptyContent>
      </Empty>
    );
  if (!selected.length && !locked)
    return (
      <Empty className="border">
        <EmptyHeader>
          <EmptyTitle>No repositories available</EmptyTitle>
          <EmptyDescription>
            Choose which repositories the panel may use.
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <ButtonLink to="/settings/repositories">
            Choose repositories
          </ButtonLink>
        </EmptyContent>
      </Empty>
    );

  return (
    <>
      {error && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription>
            Couldn’t refresh repositories. The list may be out of date.
          </AlertDescription>
        </Alert>
      )}
      <Card>
        <CardHeader>
          <CardTitle>Repository</CardTitle>
          <CardDescription>
            The repository this project deploys.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Field>
            <FieldLabel htmlFor="repository">Repository</FieldLabel>
            <Select
              items={items}
              value={repositoryId || null}
              onValueChange={choose}
              disabled={locked || !!error}
            >
              <SelectTrigger id="repository" className="w-full">
                <SelectValue placeholder="Choose a repository…" />
              </SelectTrigger>
              <SelectContent>
                {items.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FieldDescription>
              Missing one?{" "}
              <Link to="/settings/repositories">Choose repositories</Link>.
            </FieldDescription>
          </Field>
        </CardContent>
      </Card>
      {repositoryId && !repository && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription>
            That repository is no longer available. Choose another one.
          </AlertDescription>
        </Alert>
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
