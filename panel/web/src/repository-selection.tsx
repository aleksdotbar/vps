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
import { Banner, Button, Card, Empty, PageHeader } from "./components";
import { useSessionExpiry } from "./use-session-expiry";

export function RepositorySelection() {
  const integration = useSuspenseQuery(integrationQuery()).data;
  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link key="settings" to="/settings">
            Settings
          </Link>,
        ]}
        title="Repositories"
        description="Choose which installed repositories the panel may deploy."
      />
      <div className="stack" style={{ maxWidth: "40rem" }}>
        {integration.installed ? (
          <SelectionForm />
        ) : (
          <Card>
            <Empty
              title="Connect GitHub first"
              actions={
                <Link to="/settings" hash="github">
                  Open settings
                </Link>
              }
            />
          </Card>
        )}
      </div>
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
      setNotice("Saved.");
      try {
        await queryClient.cancelQueries({
          queryKey: repositoriesQuery().queryKey,
        });
        await queryClient.fetchQuery({ ...repositoriesQuery(), staleTime: 0 });
      } catch (refreshError) {
        if (!(await expireSession(refreshError)))
          setFailure("Saved, but the list couldn’t reload. Refresh the page.");
      }
    } catch (saveError) {
      if (await expireSession(saveError)) return;
      if (mutationRejected(saveError)) {
        setIntent(undefined);
        setFailure(
          "The selection was refused. Choose at least one repository.",
        );
      } else {
        setFailure(
          "We couldn’t confirm the save. Retry — it won’t apply twice.",
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
      <Card>
        <Empty title="Too many repositories">
          Limit the GitHub App installation to at most 100 repositories.
        </Empty>
      </Card>
    );
  if (!data.repositories.length && !intent)
    return (
      <Card>
        <Empty title="No repositories installed">
          Give the GitHub App access to your repositories in Settings, then come
          back here.
        </Empty>
      </Card>
    );

  return (
    <>
      {error && (
        <Banner tone="warning">
          Couldn’t refresh repositories. Reload the page before editing.
        </Banner>
      )}
      <form className="form" onSubmit={submit} aria-busy={pending}>
        <Card
          title="Available repositories"
          description="Unchecking one stops deployments of its project."
          labelledBy="repositories-title"
          flush
        >
          <fieldset
            key={selectionKey}
            disabled={pending || !!intent || !!error}
          >
            <legend className="visually-hidden">Repositories</legend>
            <ul className="list">
              {data.repositories.map((repository) => (
                <li key={String(repository.githubRepositoryId)}>
                  <label className="checkbox list-row">
                    <input
                      type="checkbox"
                      name="repository"
                      value={String(repository.githubRepositoryId)}
                      defaultChecked={selectedIds.includes(
                        String(repository.githubRepositoryId),
                      )}
                    />
                    <span className="checkbox-text">
                      {repository.owner}/{repository.name}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
          </fieldset>
        </Card>
        {failure && (
          <p className="field-error" role="alert">
            {failure}
          </p>
        )}
        {notice && (
          <p className="help" role="status">
            {notice}
          </p>
        )}
        <div className="form-actions">
          {intent ? (
            <Button
              type="button"
              variant="primary"
              pending={pending}
              onClick={() => void save(intent)}
            >
              Retry
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              pending={pending}
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
