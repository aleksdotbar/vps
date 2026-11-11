import { Button } from "@cloudflare/kumo/components/button";
import { Code, ConnectError } from "@connectrpc/connect";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";
import { z } from "zod";

import type { Repository } from "../../gen/ts/panel/v1/panel_pb";
import {
  mutationRejected,
  panel,
  projectsQuery,
  queryClient,
  workflowsQuery,
} from "./api";
import { useSessionExpiry } from "./use-session-expiry";
import { usePageContinuation } from "./use-page-continuation";

export function ProjectForm({
  repository,
  cursor,
  onLockChange,
}: {
  repository: Repository;
  cursor: string;
  onLockChange: (value: boolean) => void;
}) {
  const router = useRouter();
  const { capturePage, isMounted } = usePageContinuation();
  const expireSession = useSessionExpiry();
  const { data, error, refetch, isFetching } = useSuspenseQuery(
    workflowsQuery(repository.githubRepositoryId, cursor),
  );
  const workflows = data.workflows.filter(
    (workflow) =>
      workflow.state === "active" &&
      workflow.path === ".github/workflows/panel-build.yaml",
  );
  const [pending, setPending] = useState(false);
  const [intent, setIntent] =
    useState<Parameters<typeof panel.createProject>[0]>();
  const [failure, setFailure] = useState("");
  const [createdId, setCreatedId] = useState("");

  async function create(request: Parameters<typeof panel.createProject>[0]) {
    if (pending || createdId || router.state.status === "pending") return;
    const isCurrent = capturePage();
    setPending(true);
    onLockChange(true);
    setIntent(request);
    setFailure("");
    try {
      const result = await panel.createProject(request);
      if (!result.project) throw new Error("Missing created project");
      const projectId = z.uuid().parse(result.project.id);
      if (isCurrent()) {
        setCreatedId(projectId);
        setIntent(undefined);
      }
      try {
        await queryClient.invalidateQueries({
          queryKey: projectsQuery().queryKey,
        });
        if (!isCurrent()) return;
        await router.navigate({
          to: "/projects/$projectId",
          params: { projectId },
          search: { cursor: "" },
        });
      } catch (navigationError) {
        if (!isCurrent()) return;
        if (!(await expireSession(navigationError)) && isCurrent())
          setFailure(
            "Project created, but its details could not be opened. Use Open project to try again.",
          );
      }
    } catch (createError) {
      if (!isCurrent()) return;
      if (await expireSession(createError)) return;
      if (!isCurrent()) return;
      if (mutationRejected(createError)) {
        setIntent(undefined);
        onLockChange(false);
        setFailure(
          createError instanceof ConnectError &&
            createError.code === Code.AlreadyExists
            ? "This repository already has a project. Return to Projects to open it."
            : "Creation was refused. Check repository access and the active panel-build.yaml workflow, then refresh this page.",
        );
      } else {
        setFailure(
          "Creation outcome unknown. Check the same request to recover its result without creating a duplicate.",
        );
      }
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || intent || createdId || error) return;
    const values = new FormData(event.currentTarget);
    const workflow = workflows.find(
      (entry) => String(entry.id) === values.get("workflow"),
    );
    if (!workflow) return;
    void create({
      mutation: { requestId: crypto.randomUUID() },
      name: String(values.get("name") ?? ""),
      repository,
      githubWorkflowId: workflow.id,
    });
  }

  return (
    <section aria-labelledby="project-workflow-title">
      <h2 id="project-workflow-title">Build workflow</h2>
      <p>
        Commit an active .github/workflows/panel-build.yaml workflow before
        creating this project. Builds run in GitHub Actions; creating a project
        does not deploy it.
      </p>
      {error && (
        <p role="alert">
          Workflow refresh failed. Refresh before creating the project.
        </p>
      )}
      {workflows.length || intent ? (
        <form className="settings-form" onSubmit={submit} aria-busy={pending}>
          <label htmlFor="project-name">Project name</label>
          <input
            id="project-name"
            name="name"
            defaultValue={repository.name}
            required
            maxLength={100}
            readOnly={pending || !!intent || !!createdId || !!error}
          />
          <label htmlFor="project-workflow">GitHub workflow</label>
          <select
            id="project-workflow"
            name="workflow"
            required
            disabled={pending || !!intent || !!createdId || !!error}
          >
            {workflows.map((workflow) => (
              <option value={String(workflow.id)} key={String(workflow.id)}>
                {workflow.name} ({workflow.path})
              </option>
            ))}
          </select>
          {intent ? (
            <Button
              type="button"
              disabled={pending}
              onClick={() => void create(intent)}
            >
              Check creation result
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              disabled={pending || !!createdId || !!error}
            >
              Create project
            </Button>
          )}
        </form>
      ) : (
        <p className="notice">
          No eligible workflow on this page. Commit or activate
          panel-build.yaml, then refresh workflows.
        </p>
      )}
      {failure && (
        <p className="form-error" role="alert">
          {failure}
        </p>
      )}
      {createdId && (
        <p role="status">
          Project created.{" "}
          <Link
            to="/projects/$projectId"
            params={{ projectId: createdId }}
            search={{ cursor: "" }}
          >
            Open project
          </Link>
          .
        </p>
      )}
      <Button
        variant="secondary"
        disabled={pending || isFetching || !!intent}
        onClick={() => void refetch()}
      >
        Refresh workflows
      </Button>
      <nav className="pagination" aria-label="Workflow pages">
        {cursor && !pending && !intent && !createdId && (
          <Link
            to="/projects/new"
            search={{
              repository: String(repository.githubRepositoryId),
              workflows: "",
            }}
          >
            First workflow page
          </Link>
        )}
        {data.page?.nextPageToken && !pending && !intent && !createdId && (
          <Link
            to="/projects/new"
            search={{
              repository: String(repository.githubRepositoryId),
              workflows: data.page.nextPageToken,
            }}
          >
            Next workflow page
          </Link>
        )}
      </nav>
    </section>
  );
}
