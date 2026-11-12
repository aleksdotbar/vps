import { Button } from "@cloudflare/kumo/components/button";
import { Code, ConnectError } from "@connectrpc/connect";
import { Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";
import { z } from "zod";

import type { Repository } from "../../gen/ts/panel/v1/panel_pb";
import { mutationRejected, panel, projectsQuery, queryClient } from "./api";
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
            : "Creation was refused. Check repository access, then refresh this page.",
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
    if (pending || intent || createdId) return;
    const values = new FormData(event.currentTarget);
    void create({
      mutation: { requestId: crypto.randomUUID() },
      name: String(values.get("name") ?? ""),
      repository,
    });
  }

  return (
    <section aria-labelledby="project-images-title">
      <h2 id="project-images-title">Images</h2>
      <p>
        Your CI pushes one image per service tagged with the full commit SHA. In
        Compose, write each image as{" "}
        <code>ghcr.io/owner/name:${"{GIT_SHA}"}</code>; the panel deploys the
        digest of that tag. Creating a project does not deploy it.
      </p>
      <form className="settings-form" onSubmit={submit} aria-busy={pending}>
        <label htmlFor="project-name">Project name</label>
        <input
          id="project-name"
          name="name"
          defaultValue={repository.name}
          required
          maxLength={100}
          readOnly={pending || !!intent || !!createdId}
        />
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
            disabled={pending || !!createdId}
          >
            Create project
          </Button>
        )}
      </form>
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
    </section>
  );
}
