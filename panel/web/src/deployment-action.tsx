import { Code, ConnectError } from "@connectrpc/connect";
import { Link, useRouter } from "@tanstack/react-router";
import { useRef, useState } from "react";
import type { ReactNode } from "react";
import { z } from "zod";

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
import {
  Banner,
  Button,
  Card,
  Detail,
  Details,
  Id,
  ObservedTime,
} from "./components";
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
  const { capturePage, isMounted } = usePageContinuation();
  const running = useRef(false);
  const [pending, setPending] = useState(false);
  // shortcut: unresolved intent survives only this page; persist it if recovery across reloads is required.
  const [selection, setSelection] = useState<Selection>();
  const [unknown, setUnknown] = useState(false);
  const [acceptedId, setAcceptedId] = useState("");
  const [failure, setFailure] = useState("");
  const stale =
    !!selection && environment.version !== selection.environment.version;
  const requestId = selection?.intent.request.mutation?.requestId;

  async function prepare() {
    if (
      running.current ||
      unknown ||
      acceptedId ||
      router.state.status === "pending"
    )
      return;
    const isCurrent = capturePage();
    running.current = true;
    setPending(true);
    setFailure("");
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
        setFailure(deploymentError(error));
    } finally {
      running.current = false;
      if (isMounted()) setPending(false);
    }
  }

  async function accepted(
    id: string,
    selected: Selection,
    isCurrent: () => boolean,
  ) {
    if (isMounted()) {
      setAcceptedId(id);
      setUnknown(false);
    }
    try {
      await invalidateDeploymentMetadata(selected.environment);
      if (isCurrent())
        await router.navigate({
          to: "/operations/$operationId",
          params: { operationId: id },
        });
    } catch (error) {
      if (isCurrent() && !(await expireSession(error)) && isCurrent())
        setFailure(
          "Deployment started, but its status page didn’t open. Use the link below.",
        );
    }
  }

  async function submit() {
    if (
      !selection ||
      running.current ||
      acceptedId ||
      (!unknown && (stale || refreshFailed)) ||
      router.state.status === "pending"
    )
      return;
    const selected = selection;
    const wasUnknown = unknown;
    const isCurrent = capturePage();
    running.current = true;
    setPending(true);
    setFailure("");
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
      if (!isMounted()) return;
      if (isCurrent() && (await expireSession(error))) return;
      if (mutationRejected(error) && !wasUnknown) {
        setSelection(undefined);
        setFailure(deploymentError(error));
      } else {
        setUnknown(true);
        setFailure(
          wasUnknown && mutationRejected(error)
            ? `Retry refused — the first request may still have started. ${deploymentError(error)}`
            : "We couldn’t confirm whether this started. Check its status or retry the same request — it won’t run twice.",
        );
      }
    } finally {
      running.current = false;
      if (isMounted()) setPending(false);
    }
  }

  async function check() {
    if (
      !selection ||
      !requestId ||
      running.current ||
      router.state.status === "pending"
    )
      return;
    const isCurrent = capturePage();
    running.current = true;
    setPending(true);
    setFailure("");
    try {
      const result = await panel.getOperation({ id: requestId });
      if (result.operation?.id !== requestId)
        throw new Error("Operation identity unavailable");
      await accepted(requestId, selection, isCurrent);
    } catch (error) {
      if (isCurrent() && !(await expireSession(error)) && isCurrent())
        setFailure(
          error instanceof ConnectError && error.code === Code.NotFound
            ? "Not visible yet — it may still be processing. Check again or retry the same request."
            : `Couldn’t check the status. ${deploymentError(error)}`,
        );
    } finally {
      running.current = false;
      if (isMounted()) setPending(false);
    }
  }

  const name =
    selection?.environment.settings?.name ?? environment.settings?.name;
  const trigger = acceptedId ? null : (
    <Button
      variant={release ? "secondary" : "primary"}
      pending={pending && !selection}
      disabled={pending || !!selection}
      onClick={() => void prepare()}
    >
      {release ? "Roll back to this release" : "Deploy"}
    </Button>
  );

  const review = (
    <>
      {selection && !acceptedId && (
        <Card
          title={release ? `Roll back ${name}?` : `Deploy to ${name}?`}
          labelledBy="deploy-review-title"
          footer={
            unknown ? (
              <>
                <Button
                  variant="primary"
                  pending={pending}
                  onClick={() => void check()}
                >
                  Check status
                </Button>
                <Button disabled={pending} onClick={() => void submit()}>
                  Retry same request
                </Button>
              </>
            ) : (
              <>
                <Button
                  variant="primary"
                  pending={pending}
                  disabled={stale || refreshFailed}
                  onClick={() => void submit()}
                >
                  {release ? "Roll back" : "Deploy now"}
                </Button>
                <Button
                  variant="ghost"
                  disabled={pending}
                  onClick={() => {
                    setSelection(undefined);
                    setFailure("");
                  }}
                >
                  Cancel
                </Button>
              </>
            )
          }
        >
          <p>
            {release ? (
              <>
                Restores this release’s images, configuration, domains and
                secrets. Databases and other data are not rolled back.
              </>
            ) : (
              <>
                Deploys the latest commit on{" "}
                <code>{selection.environment.settings?.branch}</code> with the
                current settings and secrets. If its images aren’t built yet,
                the deployment waits for them.
              </>
            )}{" "}
            <span className="muted">
              Services restart, so expect a brief interruption.
            </span>
          </p>
          <Details>
            <Detail label="Commit">
              <Id value={selection.source} short label="commit" />
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
            <Banner
              tone="warning"
              actions={
                <Button
                  size="sm"
                  pending={pending}
                  onClick={() => void prepare()}
                >
                  Review again
                </Button>
              }
            >
              The environment changed while you were reviewing.
            </Banner>
          )}
          {unknown && (
            <Banner tone="warning" title="Outcome not confirmed">
              Keep this page open until it’s resolved. Request{" "}
              <Id value={requestId ?? ""} short label="request ID" />
            </Banner>
          )}
          {failure && (
            <p className="field-error" role="alert">
              {failure}
            </p>
          )}
        </Card>
      )}
      {!selection && failure && <Banner tone="danger">{failure}</Banner>}
      {acceptedId && (
        <Banner tone="info" role="status">
          {release ? "Rollback" : "Deployment"} started.{" "}
          <Link
            to="/operations/$operationId"
            params={{ operationId: acceptedId }}
          >
            Follow its progress
          </Link>
        </Banner>
      )}
    </>
  );

  return children({ trigger, panel: review });
}
