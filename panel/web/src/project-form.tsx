import { Code, ConnectError } from "@connectrpc/connect";
import { Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";
import { z } from "zod";

import type { Repository } from "../../gen/ts/panel/v1/panel_pb";
import { mutationRejected, panel, projectsQuery, queryClient } from "./api";
import { Banner, Button, Card } from "./components";
import { useSessionExpiry } from "./use-session-expiry";
import { usePageContinuation } from "./use-page-continuation";

export function ProjectForm({
  repository,
  onLockChange,
}: {
  repository: Repository;
  onLockChange: (value: boolean) => void;
}) {
  const router = useRouter();
  const { capturePage, isMounted } = usePageContinuation();
  const expireSession = useSessionExpiry();
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
            "Project created, but it didn’t open. Use the link below.",
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
            ? "This repository already has a project."
            : "The project couldn’t be created. Check repository access.",
        );
      } else {
        setFailure(
          "We couldn’t confirm this. Check again — it won’t create a duplicate.",
        );
      }
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || intent || createdId) return;
    const values = new FormData(event.currentTarget);
    void create({
      mutation: { requestId: crypto.randomUUID() },
      name: String(values.get("name") ?? ""),
      repository,
    });
  }

  return (
    <Card title="2. Project" labelledBy="project-step-title">
      <form className="form" onSubmit={submit} aria-busy={pending}>
        <div className="field">
          <label htmlFor="project-name">Name</label>
          <input
            id="project-name"
            name="name"
            defaultValue={repository.name}
            required
            maxLength={100}
            readOnly={pending || !!intent || !!createdId}
          />
        </div>
        <Banner tone="info" title="How builds work">
          Your CI pushes one image per service, tagged with the commit SHA. In{" "}
          <code>compose.yaml</code>, reference it as{" "}
          <code>ghcr.io/owner/name:${"{GIT_SHA}"}</code>. Creating the project
          doesn’t deploy anything yet.
        </Banner>
        {failure && (
          <p className="field-error" role="alert">
            {failure}
          </p>
        )}
        <div className="form-actions">
          {intent ? (
            <Button
              type="button"
              variant="primary"
              pending={pending}
              onClick={() => void create(intent)}
            >
              Check result
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              pending={pending}
              disabled={!!createdId}
            >
              Create project
            </Button>
          )}
          {createdId && (
            <Link
              to="/projects/$projectId"
              params={{ projectId: createdId }}
              search={{ cursor: "" }}
            >
              Open project
            </Link>
          )}
        </div>
      </form>
    </Card>
  );
}
