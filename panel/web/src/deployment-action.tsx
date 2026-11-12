import { Button } from "@cloudflare/kumo/components/button";
import { Code, ConnectError } from "@connectrpc/connect";
import { Link, useRouter } from "@tanstack/react-router";
import { useRef, useState } from "react";
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
import { Detail, Details } from "./components";
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
      return "Settings or secrets changed. Review the latest environment before submitting a new request.";
    case ErrorReason.OPERATION_CONFLICT:
      return "An unresolved operation blocks this action. Open operation history and wait for reconciliation.";
    case ErrorReason.SERVER_NOT_READY:
      return "The assigned server is not ready. Open its details and resolve setup or connectivity first.";
    case ErrorReason.INCOMPATIBLE_CONFIGURATION:
      return "Check the GitHub installation, the commit's Compose files and images, the retained release assignment and required Tunnel setup.";
    case ErrorReason.REQUEST_ID_REUSED:
      return "The request identity conflicts with an existing intent. Check operation history before starting another action.";
    case ErrorReason.RESOURCE_IN_USE:
      return "A retained resource conflicts with this action or operation capacity is full. Check operation history and environment configuration.";
    default:
      return errorMessage(error);
  }
}

export function DeploymentAction({
  environment,
  release,
  refreshFailed,
}: {
  environment: Environment;
  release?: Release;
  refreshFailed: boolean;
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
  const title = release ? "Rollback environment" : "Deploy environment";
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
          "Request accepted, but status could not be opened. Use Open operation to check its result.",
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
        setFailure(
          `Request refused. ${deploymentError(error)} Review current metadata before trying again.`,
        );
      } else {
        setUnknown(true);
        setFailure(
          wasUnknown && mutationRejected(error)
            ? `Retry refused. The earlier request may still have been accepted. ${deploymentError(error)}`
            : "Request outcome unknown. Check operation status or retry the exact saved request. Do not start another deployment or rollback until resolved.",
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
            ? "No operation is visible yet. The original request may still be processing. Keep this request and check again or retry it unchanged."
            : `Status could not be checked. ${deploymentError(error)} The request remains unresolved.`,
        );
    } finally {
      running.current = false;
      if (isMounted()) setPending(false);
    }
  }

  return (
    <section
      aria-labelledby={release ? "rollback-title" : "deploy-title"}
      aria-busy={pending}
    >
      <h2 id={release ? "rollback-title" : "deploy-title"}>{title}</h2>
      {!selection && !acceptedId && (
        <Button
          variant="primary"
          disabled={pending}
          onClick={() => void prepare()}
        >
          {pending
            ? "Loading selection…"
            : release
              ? "Review rollback"
              : "Deploy…"}
        </Button>
      )}
      {selection && !acceptedId && (
        <>
          <p>
            {release
              ? "Restore this release's images, Compose configuration, domains and secret versions for the entire environment. Brief downtime is possible. Database contents, external effects and revoked credentials are not restored."
              : "Deploy this selected branch revision with the captured settings and secrets for the entire environment. GitHub Actions may need to build it first. Brief downtime is possible."}
          </p>
          <Details>
            <Detail label="Environment">
              {selection.environment.settings?.name || selection.environment.id}
            </Detail>
            <Detail label="Server">
              <code>{selection.environment.settings?.serverId}</code>
            </Detail>
            <Detail label={release ? "Release source" : "Source SHA"}>
              <code>{selection.source}</code>
            </Detail>
            <Detail label="Branch">
              {release
                ? release.build?.branch || "Unavailable"
                : selection.environment.settings?.branch}
            </Detail>
            <Detail label="Settings version">
              {release
                ? release.environmentVersion.toString()
                : selection.environment.version.toString()}
            </Detail>
            <Detail label="Secret version">
              {release
                ? release.secretVersionId
                : selection.environment.latestSecretVersionId || "None"}
            </Detail>
            {release && (
              <Detail label="Selected release">
                <code>{release.id}</code>
              </Detail>
            )}
          </Details>
          {!unknown && (stale || refreshFailed) && (
            <p role="alert">
              Environment metadata changed or refresh failed. Review the current
              selection before confirming.
            </p>
          )}
          {unknown && (
            <p className="notice">
              This exact request is retained while this page remains open. Keep
              it open until resolved. Request ID: <code>{requestId}</code>
            </p>
          )}
          <div className="form-actions">
            {unknown ? (
              <>
                <Button disabled={pending} onClick={() => void check()}>
                  Check operation status
                </Button>
                <Button
                  variant="secondary"
                  disabled={pending}
                  onClick={() => void submit()}
                >
                  Retry same request
                </Button>
              </>
            ) : (
              <>
                <Button
                  variant="primary"
                  disabled={pending || stale || refreshFailed}
                  onClick={() => void submit()}
                >
                  {pending
                    ? "Submitting…"
                    : release
                      ? "Confirm whole-environment rollback"
                      : "Confirm deployment"}
                </Button>
                <Button
                  variant="secondary"
                  disabled={pending}
                  onClick={() => void prepare()}
                >
                  Refresh selection
                </Button>
                <Button
                  variant="secondary"
                  disabled={pending}
                  onClick={() => {
                    setSelection(undefined);
                    setFailure("");
                  }}
                >
                  Cancel
                </Button>
              </>
            )}
          </div>
        </>
      )}
      {failure && (
        <p className="form-error" role="alert">
          {failure}
        </p>
      )}
      {acceptedId && (
        <p role="status">
          Request accepted. Deployment success is not yet confirmed.{" "}
          <Link
            to="/operations/$operationId"
            params={{ operationId: acceptedId }}
          >
            Open operation
          </Link>
        </p>
      )}
      <p className="action-link">
        <Link
          to="/operations"
          search={{ environment: environment.id, server: "", cursor: "" }}
        >
          Environment operation history
        </Link>
      </p>
    </section>
  );
}
