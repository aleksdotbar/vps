import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { InfoIcon } from "lucide-react";
import { useState } from "react";
import type { FormEvent } from "react";
import { toast } from "sonner";
import { z } from "zod";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

import type { Repository } from "../../gen/ts/panel/v1/panel_pb";
import { mutationRejected, panel, projectsQuery, queryClient } from "./api";
import { PendingButton } from "./components";
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
          toast.error(
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
        toast.error(
          createError instanceof ConnectError &&
            createError.code === Code.AlreadyExists
            ? "This repository already has a project."
            : "The project couldn’t be created. Check repository access.",
        );
      } else {
        toast.error(
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
    <Card>
      <CardHeader>
        <CardTitle>Project</CardTitle>
        <CardDescription>
          Name it; you can add environments next.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} aria-busy={pending}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="project-name">Name</FieldLabel>
              <Input
                id="project-name"
                name="name"
                defaultValue={repository.name}
                required
                maxLength={100}
                readOnly={pending || !!intent || !!createdId}
              />
            </Field>
            <Alert>
              <InfoIcon />
              <AlertTitle>How builds work</AlertTitle>
              <AlertDescription>
                <p>
                  Your CI pushes one image per service, tagged with the commit
                  SHA. In <code>compose.yaml</code>, reference it as{" "}
                  <code>ghcr.io/owner/name:${"{GIT_SHA}"}</code>. Creating the
                  project doesn’t deploy anything yet.
                </p>
              </AlertDescription>
            </Alert>
            <Field orientation="horizontal">
              {intent ? (
                <PendingButton
                  type="button"
                  pending={pending}
                  onClick={() => run(intent)}
                >
                  Check result
                </PendingButton>
              ) : (
                <PendingButton
                  type="submit"
                  pending={pending}
                  disabled={!!createdId}
                >
                  Create project
                </PendingButton>
              )}
              {createdId && (
                <Link
                  to="/projects/$projectId"
                  params={{ projectId: createdId }}
                  search={{ cursor: "" }}
                  className="text-sm font-medium underline-offset-4 hover:underline"
                >
                  Open project
                </Link>
              )}
            </Field>
          </FieldGroup>
        </form>
      </CardContent>
    </Card>
  );
}
