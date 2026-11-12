import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { TriangleAlertIcon } from "lucide-react";
import { useState } from "react";
import type { FormEvent } from "react";
import { toast } from "sonner";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";

import {
  github,
  integrationQuery,
  mutationRejected,
  queryClient,
  repositoriesQuery,
} from "./api";
import { ButtonLink, Page, PendingButton } from "./components";
import { useSessionExpiry } from "./use-session-expiry";

export function RepositorySelection() {
  const integration = useSuspenseQuery(integrationQuery()).data;
  return (
    <Page
      title="Repositories"
      description="Choose which installed repositories the panel may deploy."
      crumbs={[{ label: "Settings", link: { to: "/settings" } }]}
    >
      <div className="flex max-w-2xl flex-col gap-6">
        {integration.installed ? (
          <SelectionForm />
        ) : (
          <Empty className="border">
            <EmptyHeader>
              <EmptyTitle>Connect GitHub first</EmptyTitle>
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

function SelectionForm() {
  const { data, error, refetch, isFetching } =
    useSuspenseQuery(repositoriesQuery());
  const expireSession = useSessionExpiry();
  const [intent, setIntent] =
    useState<Parameters<typeof github.selectRepositories>[0]>();
  const [selectionError, setSelectionError] = useState("");
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
  const locked = pending || !!intent || !!error;

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
          toast.error("Saved, but the list couldn’t reload. Refresh the page.");
      }
    } catch (saveError) {
      if (await expireSession(saveError)) return;
      if (mutationRejected(saveError)) {
        setIntent(undefined);
        toast.error(
          "The selection was refused. Choose at least one repository.",
        );
      } else {
        toast.error(
          "We couldn’t confirm the save. Retry — it won’t apply twice.",
        );
      }
    }
  }

  function toggle(id: string, checked: boolean) {
    setSelectionError("");
    setChosen({
      key: selectionKey,
      ids: checked
        ? [...chosen.ids, id]
        : chosen.ids.filter((value) => value !== id),
    });
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
      <Empty className="border">
        <EmptyHeader>
          <EmptyTitle>Too many repositories</EmptyTitle>
          <EmptyDescription>
            Limit the GitHub App installation to at most 100 repositories.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  if (!data.repositories.length && !intent)
    return (
      <Empty className="border">
        <EmptyHeader>
          <EmptyTitle>No repositories installed</EmptyTitle>
          <EmptyDescription>
            Give the GitHub App access to your repositories in Settings, then
            come back here.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );

  return (
    <>
      {error && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription>
            Couldn’t refresh repositories. Reload the page before editing.
          </AlertDescription>
        </Alert>
      )}
      <form onSubmit={submit} aria-busy={pending}>
        <FieldGroup>
          <Card>
            <CardContent>
              <FieldSet data-invalid={!!selectionError || undefined}>
                <FieldLegend>Available repositories</FieldLegend>
                <FieldDescription>
                  Unchecking one stops deployments of its project.
                </FieldDescription>
                <FieldGroup className="gap-3">
                  {data.repositories.map((repository) => {
                    const id = String(repository.githubRepositoryId);
                    return (
                      <Field key={id} orientation="horizontal">
                        <Checkbox
                          id={`repository-${id}`}
                          checked={chosen.ids.includes(id)}
                          onCheckedChange={(checked) => toggle(id, checked)}
                          disabled={locked}
                          aria-invalid={!!selectionError || undefined}
                        />
                        <FieldLabel
                          htmlFor={`repository-${id}`}
                          className="font-normal"
                        >
                          {repository.owner}/{repository.name}
                        </FieldLabel>
                      </Field>
                    );
                  })}
                </FieldGroup>
                <FieldError>{selectionError}</FieldError>
              </FieldSet>
            </CardContent>
          </Card>
          <Field orientation="horizontal">
            {intent ? (
              <PendingButton
                type="button"
                pending={pending}
                onClick={() => saving.mutate(intent)}
              >
                Retry
              </PendingButton>
            ) : (
              <PendingButton type="submit" pending={pending} disabled={!!error}>
                Save
              </PendingButton>
            )}
            <Button
              type="button"
              variant="ghost"
              disabled={pending || !!intent || isFetching}
              onClick={() => void refetch()}
            >
              Reload list
            </Button>
          </Field>
        </FieldGroup>
      </form>
    </>
  );
}
