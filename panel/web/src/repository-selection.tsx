import { Button } from "@cloudflare/kumo/components/button";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";

import {
  github,
  integrationQuery,
  mutationRejected,
  queryClient,
  repositoriesQuery,
} from "./api";
import { Empty, PageTitle } from "./components";
import { useSessionExpiry } from "./use-session-expiry";

export function RepositorySelection() {
  const integration = useSuspenseQuery(integrationQuery()).data;
  return (
    <>
      <Link className="back-link" to="/settings">
        Settings
      </Link>
      <PageTitle
        title="Repository access"
        description="Choose which installed repositories the panel can use."
      />
      {integration.installed ? (
        <SelectionForm />
      ) : (
        <Empty title="Connect GitHub first">
          Register and install your GitHub App in Settings.
        </Empty>
      )}
    </>
  );
}

function SelectionForm() {
  const { data, error, refetch, isFetching } =
    useSuspenseQuery(repositoriesQuery());
  const expireSession = useSessionExpiry();
  const [intent, setIntent] =
    useState<Parameters<typeof github.selectRepositories>[0]>();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const selectedIds = Array.from(
    intent?.repositoryIds ?? data.selectedRepositoryIds,
    String,
  );
  const selectionKey = `${intent ? "request" : "confirmed"}:${selectedIds.join(",")}`;

  async function save(
    request: Parameters<typeof github.selectRepositories>[0],
  ) {
    if (pending) return;
    setPending(true);
    setIntent(request);
    setFailure("");
    setNotice("");
    try {
      await github.selectRepositories(request);
      setIntent(undefined);
      setNotice("Repository selection saved.");
      try {
        await queryClient.cancelQueries({
          queryKey: repositoriesQuery().queryKey,
        });
        await queryClient.fetchQuery({ ...repositoriesQuery(), staleTime: 0 });
      } catch (refreshError) {
        if (!(await expireSession(refreshError)))
          setFailure(
            "Saved, but repository status could not be refreshed. Reload this page before editing again.",
          );
      }
    } catch (saveError) {
      if (await expireSession(saveError)) return;
      if (mutationRejected(saveError)) {
        setIntent(undefined);
        setFailure(
          "Selection was refused. Refresh the repository list and choose at least one repository.",
        );
      } else {
        setFailure(
          "Save outcome unknown. Retry the same selection to recover its result; no new selection will be submitted.",
        );
      }
    } finally {
      setPending(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || intent || error) return;
    const values = new FormData(event.currentTarget).getAll("repository");
    const repositoryIds = data.repositories
      .filter((repository) =>
        values.includes(String(repository.githubRepositoryId)),
      )
      .map((repository) => repository.githubRepositoryId);
    if (!repositoryIds.length) {
      setFailure("Select at least one repository.");
      return;
    }
    void save({ mutation: { requestId: crypto.randomUUID() }, repositoryIds });
  }

  if (data.page?.nextPageToken && !intent)
    return (
      <Empty title="Repository list incomplete">
        Reconnect the App with at most 100 selected repositories before editing
        panel access.
      </Empty>
    );
  if (!data.repositories.length && !intent)
    return (
      <Empty title="No repositories installed">
        Update repository access on GitHub from Settings, then return here.
      </Empty>
    );

  return (
    <>
      <p className="notice">
        Only checked repositories can be used by the panel. Clearing a
        repository also blocks Actions and deployments for its existing project.
        GitHub installation access is unchanged.
      </p>
      {error && (
        <p role="alert">
          Refresh failed. Reload this page before editing repository access.
        </p>
      )}
      <form className="settings-form" onSubmit={submit} aria-busy={pending}>
        <fieldset key={selectionKey} disabled={pending || !!intent || !!error}>
          <legend>Repositories available to the panel</legend>
          {data.repositories.map((repository) => (
            <label
              className="checkbox-label"
              key={String(repository.githubRepositoryId)}
            >
              <input
                type="checkbox"
                name="repository"
                value={String(repository.githubRepositoryId)}
                defaultChecked={selectedIds.includes(
                  String(repository.githubRepositoryId),
                )}
              />
              <span>
                {repository.owner}/{repository.name}
              </span>
            </label>
          ))}
        </fieldset>
        {failure && (
          <p className="form-error" role="alert">
            {failure}
          </p>
        )}
        {notice && <p role="status">{notice}</p>}
        <div className="form-actions">
          {intent ? (
            <Button
              type="button"
              disabled={pending}
              onClick={() => void save(intent)}
            >
              Retry same selection
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              disabled={pending || !!error}
            >
              Save selection
            </Button>
          )}
          <Button
            type="button"
            variant="secondary"
            disabled={pending || isFetching || !!intent}
            onClick={() => void refetch()}
          >
            Refresh repositories
          </Button>
        </div>
      </form>
    </>
  );
}
