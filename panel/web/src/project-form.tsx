import { Code, ConnectError } from "@connectrpc/connect";
import {
  Banner,
  Button,
  Input,
  LayerCard,
  Link,
  useKumoToastManager,
} from "@cloudflare/kumo";
import { InfoIcon } from "@phosphor-icons/react";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
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
  const { capturePage } = usePageContinuation();
  const expireSession = useSessionExpiry();
  const [intent, setIntent] =
    useState<Parameters<typeof panel.createProject>[0]>();
  const toast = useKumoToastManager();
  const fail = (title: string) => toast.add({ title, variant: "error" });
  const [createdId, setCreatedId] = useState("");

  const creation = useMutation({ mutationFn: create });
  const pending = creation.isPending;

  async function create(request: Parameters<typeof panel.createProject>[0]) {
    const isCurrent = capturePage();
    onLockChange(true);
    setIntent(request);
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
          fail("Project created, but it didn’t open. Use the link below.");
      }
    } catch (createError) {
      if (!isCurrent()) return;
      if (await expireSession(createError)) return;
      if (!isCurrent()) return;
      if (mutationRejected(createError)) {
        setIntent(undefined);
        onLockChange(false);
        fail(
          createError instanceof ConnectError &&
            createError.code === Code.AlreadyExists
            ? "This repository already has a project."
            : "The project couldn’t be created. Check repository access.",
        );
      } else {
        fail(
          "We couldn’t confirm this. Check again — it won’t create a duplicate.",
        );
      }
    }
  }

  function run(request: Parameters<typeof panel.createProject>[0]) {
    if (pending || createdId || router.state.status === "pending") return;
    creation.mutate(request);
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || intent || createdId) return;
    const values = new FormData(event.currentTarget);
    run({
      mutation: { requestId: crypto.randomUUID() },
      name: String(values.get("name") ?? ""),
      repository,
    });
  }

  return (
    <LayerCard>
      <LayerCard.Secondary>2. Project</LayerCard.Secondary>
      <LayerCard.Primary>
        <form
          className="flex flex-col gap-4"
          onSubmit={submit}
          aria-busy={pending}
        >
          <Input
            label="Name"
            name="name"
            defaultValue={repository.name}
            required
            maxLength={100}
            readOnly={pending || !!intent || !!createdId}
          />
          <Banner
            icon={<InfoIcon weight="fill" />}
            title="How builds work"
            description={
              <>
                Your CI pushes one image per service, tagged with the commit
                SHA. In <code>compose.yaml</code>, reference it as{" "}
                <code>ghcr.io/owner/name:${"{GIT_SHA}"}</code>. Creating the
                project doesn’t deploy anything yet.
              </>
            }
          />
          <div className="flex items-center gap-3">
            {intent ? (
              <Button
                type="button"
                variant="primary"
                loading={pending}
                onClick={() => run(intent)}
              >
                Check result
              </Button>
            ) : (
              <Button
                type="submit"
                variant="primary"
                loading={pending}
                disabled={!!createdId}
              >
                Create project
              </Button>
            )}
            {createdId && (
              <Link href={`/projects/${createdId}`}>Open project</Link>
            )}
          </div>
        </form>
      </LayerCard.Primary>
    </LayerCard>
  );
}
