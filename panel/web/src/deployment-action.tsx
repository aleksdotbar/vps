import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { InfoIcon, TriangleAlertIcon } from "lucide-react";
import { useState } from "react";
import type { ReactNode } from "react";
import { toast } from "sonner";
import { z } from "zod";

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

import {
  type Environment,
  ErrorDetailSchema,
  ErrorReason,
  type Release,
} from "../../gen/ts/panel/v1/panel_pb";
import {
  errorMessage,
  invalidateDeploymentMetadata,
  mutationRejected,
  panel,
  refreshEnvironment,
} from "./api";
import { Detail, Details, Id, ObservedTime, PendingButton } from "./components";
import { usePageContinuation } from "./use-page-continuation";
import { useSessionExpiry } from "./use-session-expiry";

type Intent =
  | { kind: "deploy"; request: Parameters<typeof panel.deployEnvironment>[0] }
  | {
      kind: "rollback";
      request: Parameters<typeof panel.rollbackEnvironment>[0];
    };
type Selection = {
  intent: Intent;
  environment: Environment;
  source: string;
};

function deploymentError(error: unknown) {
  if (!(error instanceof ConnectError)) return errorMessage(error);
  const reason = error.findDetails(ErrorDetailSchema)[0]?.reason;
  switch (reason) {
    case ErrorReason.STALE_VERSION:
      return "Settings or secrets changed while you were reviewing. Review again before deploying.";
    case ErrorReason.OPERATION_CONFLICT:
      return "Another deployment is still in progress. Wait for it to finish in Activity.";
    case ErrorReason.SERVER_NOT_READY:
      return "The server isn’t ready. Open the server to see what needs attention.";
    case ErrorReason.INCOMPATIBLE_CONFIGURATION:
      return "This commit can’t be deployed as configured. Check the Compose files, images, GitHub access and the Cloudflare tunnel.";
    case ErrorReason.REQUEST_ID_REUSED:
      return "This request conflicts with an earlier one. Check Activity before trying again.";
    case ErrorReason.RESOURCE_IN_USE:
      return "A conflicting resource or a full queue blocks this. Check Activity and the environment settings.";
    default:
      return errorMessage(error);
  }
}

export function DeploymentAction({
  environment,
  release,
  refreshFailed,
  serverName,
  children,
}: {
  environment: Environment;
  release?: Release;
  refreshFailed: boolean;
  serverName?: string;
  /** Places the trigger (e.g. in the page header) and the review panel. */
  children: (parts: { trigger: ReactNode; panel: ReactNode }) => ReactNode;
}) {
  const router = useRouter();
  const expireSession = useSessionExpiry();
  const { capturePage } = usePageContinuation();
  // shortcut: unresolved intent survives only this page; persist it if recovery across reloads is required.
  const [selection, setSelection] = useState<Selection>();
  const [unknown, setUnknown] = useState(false);
  const [acceptedId, setAcceptedId] = useState("");
  const fail = (title: string) => toast.error(title);
  const stale =
    !!selection && environment.version !== selection.environment.version;
  const requestId = selection?.intent.request.mutation?.requestId;

  async function accepted(
    id: string,
    selected: Selection,
    isCurrent: () => boolean,
  ) {
    setAcceptedId(id);
    setUnknown(false);
    try {
      await invalidateDeploymentMetadata(selected.environment);
      if (isCurrent())
        await router.navigate({
          to: "/operations/$operationId",
          params: { operationId: id },
        });
    } catch (error) {
      if (isCurrent() && !(await expireSession(error)) && isCurrent())
        fail(
          "Deployment started, but its status page didn’t open. Use the link below.",
        );
    }
  }

  const prepare = useMutation({
    mutationFn: async () => {
      const isCurrent = capturePage();
      setSelection(undefined);
      try {
        const current = await refreshEnvironment(environment.id);
        let intent: Intent;
        let source: string;
        if (release) {
          if (release.environmentId !== current.id)
            throw new Error("Release assignment mismatch");
          intent = {
            kind: "rollback",
            request: {
              mutation: { requestId: crypto.randomUUID() },
              environmentId: current.id,
              expectedVersion: current.version,
              releaseId: release.id,
            },
          };
          source = release.build?.sourceSha || "Source metadata unavailable";
        } else {
          const result = await panel.getDeploymentSource({
            environmentId: current.id,
            expectedVersion: current.version,
          });
          source = z
            .string()
            .regex(/^[0-9a-f]{40}$/)
            .parse(result.sourceSha);
          if (
            result.environmentVersion !== current.version ||
            result.branch !== current.settings?.branch
          )
            throw new Error("Deployment source changed");
          intent = {
            kind: "deploy",
            request: {
              mutation: { requestId: crypto.randomUUID() },
              environmentId: current.id,
              expectedVersion: current.version,
              sourceSha: source,
            },
          };
        }
        if (isCurrent()) setSelection({ intent, environment: current, source });
      } catch (error) {
        if (isCurrent() && !(await expireSession(error)) && isCurrent())
          fail(deploymentError(error));
      }
    },
  });

  const submit = useMutation({
    mutationFn: async (selected: Selection) => {
      const wasUnknown = unknown;
      const isCurrent = capturePage();
      try {
        const result =
          selected.intent.kind === "deploy"
            ? await panel.deployEnvironment(selected.intent.request)
            : await panel.rollbackEnvironment(selected.intent.request);
        const id = z.uuid().parse(result.operation?.id);
        if (id !== selected.intent.request.mutation?.requestId)
          throw new Error("Operation identity mismatch");
        await accepted(id, selected, isCurrent);
      } catch (error) {
        if (isCurrent() && (await expireSession(error))) return;
        if (mutationRejected(error) && !wasUnknown) {
          setSelection(undefined);
          fail(deploymentError(error));
        } else {
          setUnknown(true);
          fail(
            wasUnknown && mutationRejected(error)
              ? `Retry refused — the first request may still have started. ${deploymentError(error)}`
              : "We couldn’t confirm whether this started. Check its status or retry the same request — it won’t run twice.",
          );
        }
      }
    },
  });

  const check = useMutation({
    mutationFn: async ({
      selected,
      id,
    }: {
      selected: Selection;
      id: string;
    }) => {
      const isCurrent = capturePage();
      try {
        const result = await panel.getOperation({ id });
        if (result.operation?.id !== id)
          throw new Error("Operation identity unavailable");
        await accepted(id, selected, isCurrent);
      } catch (error) {
        if (isCurrent() && !(await expireSession(error)) && isCurrent())
          fail(
            error instanceof ConnectError && error.code === Code.NotFound
              ? "Not visible yet — it may still be processing. Check again or retry the same request."
              : `Couldn’t check the status. ${deploymentError(error)}`,
          );
      }
    },
  });

  const pending = prepare.isPending || submit.isPending || check.isPending;
  const navigating = () => router.state.status === "pending";

  function startReview() {
    if (pending || unknown || acceptedId || navigating()) return;
    prepare.mutate();
  }

  function confirm() {
    if (
      !selection ||
      pending ||
      acceptedId ||
      (!unknown && (stale || refreshFailed)) ||
      navigating()
    )
      return;
    submit.mutate(selection);
  }

  function checkStatus() {
    if (!selection || !requestId || pending || navigating()) return;
    check.mutate({ selected: selection, id: requestId });
  }

  const name =
    selection?.environment.settings?.name ?? environment.settings?.name;
  const trigger = acceptedId ? null : (
    <PendingButton
      variant={release ? "outline" : "default"}
      pending={prepare.isPending && !selection}
      disabled={pending || !!selection}
      onClick={startReview}
    >
      {release ? "Roll back to this release" : "Deploy"}
    </PendingButton>
  );

  const review = (
    <>
      {selection && !acceptedId && (
        <Card>
          <CardHeader>
            <CardTitle>
              {release ? `Roll back ${name}?` : `Deploy to ${name}?`}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <p>
              {release ? (
                <>
                  Restores this release’s images, configuration, domains and
                  secrets. Databases and other data are not rolled back.
                </>
              ) : (
                <>
                  Deploys the latest commit on{" "}
                  <code className="font-mono">
                    {selection.environment.settings?.branch}
                  </code>{" "}
                  with the current settings and secrets. If its images aren’t
                  built yet, the deployment waits for them.
                </>
              )}{" "}
              Services restart, so expect a brief interruption.
            </p>
            <Details>
              <Detail label="Commit">
                <Id value={selection.source} short />
              </Detail>
              <Detail label="Branch">
                {release
                  ? release.build?.branch || "—"
                  : selection.environment.settings?.branch}
              </Detail>
              <Detail label="Server">{serverName ?? "Assigned server"}</Detail>
              {release && (
                <Detail label="Release from">
                  <ObservedTime value={release.createdAt} />
                </Detail>
              )}
            </Details>
            {!unknown && (stale || refreshFailed) && (
              <Alert>
                <TriangleAlertIcon />
                <AlertDescription>
                  The environment changed while you were reviewing.
                </AlertDescription>
                <AlertAction>
                  <PendingButton
                    size="sm"
                    variant="outline"
                    pending={prepare.isPending}
                    onClick={startReview}
                  >
                    Review again
                  </PendingButton>
                </AlertAction>
              </Alert>
            )}
            {unknown && (
              <Alert>
                <TriangleAlertIcon />
                <AlertTitle>Outcome not confirmed</AlertTitle>
                <AlertDescription>
                  <span>
                    Keep this page open until it’s resolved. Request{" "}
                    <Id value={requestId ?? ""} short />
                  </span>
                </AlertDescription>
              </Alert>
            )}
          </CardContent>
          <CardFooter className="flex-wrap gap-2">
            {unknown ? (
              <>
                <PendingButton
                  pending={check.isPending}
                  disabled={pending}
                  onClick={checkStatus}
                >
                  Check status
                </PendingButton>
                <PendingButton
                  variant="outline"
                  pending={submit.isPending}
                  disabled={pending}
                  onClick={confirm}
                >
                  Retry same request
                </PendingButton>
              </>
            ) : (
              <>
                <PendingButton
                  pending={submit.isPending}
                  disabled={pending || stale || refreshFailed}
                  onClick={confirm}
                >
                  {release ? "Roll back" : "Deploy now"}
                </PendingButton>
                <Button
                  variant="ghost"
                  disabled={pending}
                  onClick={() => setSelection(undefined)}
                >
                  Cancel
                </Button>
              </>
            )}
          </CardFooter>
        </Card>
      )}
      {acceptedId && (
        <Alert role="status">
          <InfoIcon />
          <AlertDescription>
            <span>
              {release ? "Rollback" : "Deployment"} started.{" "}
              <Link
                className="text-foreground underline underline-offset-4"
                to="/operations/$operationId"
                params={{ operationId: acceptedId }}
              >
                Follow its progress
              </Link>
            </span>
          </AlertDescription>
        </Alert>
      )}
    </>
  );

  return children({ trigger, panel: review });
}
