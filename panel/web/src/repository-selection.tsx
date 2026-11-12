import {
  Banner,
  Button,
  Checkbox,
  Empty,
  LayerCard,
  useKumoToastManager,
} from "@cloudflare/kumo";
import { WarningIcon } from "@phosphor-icons/react";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";

import {
  github,
  integrationQuery,
  mutationRejected,
  queryClient,
  repositoriesQuery,
} from "./api";
import { ButtonLink, Page } from "./components";
import { useSessionExpiry } from "./use-session-expiry";

export function RepositorySelection() {
  const integration = useSuspenseQuery(integrationQuery()).data;
  return (
    <Page
      title="Repositories"
      description="Choose which installed repositories the panel may deploy."
      crumbs={[{ label: "Settings", href: "/settings" }]}
    >
      <div className="flex max-w-2xl flex-col gap-6">
        {integration.installed ? (
          <SelectionForm />
        ) : (
          <Empty
            title="Connect GitHub first"
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

function SelectionForm() {
  const { data, error, refetch, isFetching } =
    useSuspenseQuery(repositoriesQuery());
  const expireSession = useSessionExpiry();
  const [intent, setIntent] =
    useState<Parameters<typeof github.selectRepositories>[0]>();
  const [selectionError, setSelectionError] = useState("");
  const toast = useKumoToastManager();
  const fail = (title: string) => toast.add({ title, variant: "error" });
  const selectedIds = Array.from(
    intent?.repositoryIds ?? data.selectedRepositoryIds,
    String,
  );
  const selectionKey = `${intent ? "request" : "confirmed"}:${selectedIds.join(",")}`;
  // Reset the checkboxes whenever the confirmed or requested selection changes.
  const [chosen, setChosen] = useState({ key: selectionKey, ids: selectedIds });
  if (chosen.key !== selectionKey)
    setChosen({ key: selectionKey, ids: selectedIds });
  const saving = useMutation({ mutationFn: save });
  const pending = saving.isPending;

  async function save(
    request: Parameters<typeof github.selectRepositories>[0],
  ) {
    setIntent(request);
    try {
      await github.selectRepositories(request);
      setIntent(undefined);
      try {
        await queryClient.cancelQueries({
          queryKey: repositoriesQuery().queryKey,
        });
        await queryClient.fetchQuery({ ...repositoriesQuery(), staleTime: 0 });
      } catch (refreshError) {
        if (!(await expireSession(refreshError)))
          fail("Saved, but the list couldn’t reload. Refresh the page.");
      }
    } catch (saveError) {
      if (await expireSession(saveError)) return;
      if (mutationRejected(saveError)) {
        setIntent(undefined);
        fail("The selection was refused. Choose at least one repository.");
      } else {
        fail("We couldn’t confirm the save. Retry — it won’t apply twice.");
      }
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || intent || error) return;
    const repositoryIds = data.repositories
      .filter((repository) =>
        chosen.ids.includes(String(repository.githubRepositoryId)),
      )
      .map((repository) => repository.githubRepositoryId);
    if (!repositoryIds.length) {
      setSelectionError("Select at least one repository.");
      return;
    }
    setSelectionError("");
    saving.mutate({
      mutation: { requestId: crypto.randomUUID() },
      repositoryIds,
    });
  }

  if (data.page?.nextPageToken && !intent)
    return (
      <Empty
        title="Too many repositories"
        description="Limit the GitHub App installation to at most 100 repositories."
      />
    );
  if (!data.repositories.length && !intent)
    return (
      <Empty
        title="No repositories installed"
        description="Give the GitHub App access to your repositories in Settings, then come back here."
      />
    );

  return (
    <>
      {error && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="Couldn’t refresh repositories. Reload the page before editing."
        />
      )}
      <form
        className="flex flex-col gap-4"
        onSubmit={submit}
        aria-busy={pending}
      >
        <LayerCard>
          <LayerCard.Secondary>Available repositories</LayerCard.Secondary>
          <LayerCard.Primary>
            <Checkbox.Group
              legend="Unchecking one stops deployments of its project."
              value={chosen.ids}
              error={selectionError || undefined}
              onValueChange={(ids) => setChosen({ key: selectionKey, ids })}
              disabled={pending || !!intent || !!error}
            >
              {data.repositories.map((repository) => (
                <Checkbox.Item
                  key={String(repository.githubRepositoryId)}
                  value={String(repository.githubRepositoryId)}
                  label={`${repository.owner}/${repository.name}`}
                />
              ))}
            </Checkbox.Group>
          </LayerCard.Primary>
        </LayerCard>
        <div className="flex items-center gap-2">
          {intent ? (
            <Button
              type="button"
              variant="primary"
              loading={pending}
              onClick={() => saving.mutate(intent)}
            >
              Retry
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              loading={pending}
              disabled={!!error}
            >
              Save
            </Button>
          )}
          <Button
            type="button"
            variant="ghost"
            disabled={pending || !!intent || isFetching}
            onClick={() => void refetch()}
          >
            Reload list
          </Button>
        </div>
      </form>
    </>
  );
}
