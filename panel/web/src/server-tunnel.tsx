import { Button } from "@cloudflare/kumo/components/button";
import { Code, ConnectError } from "@connectrpc/connect";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";

import { ServerTunnelPhase } from "../../gen/ts/panel/v1/cloudflare_pb";
import type { Server } from "../../gen/ts/panel/v1/panel_pb";
import {
  cloudflare,
  cloudflareIntegrationQuery,
  mutationRejected,
  queryClient,
  serverTunnelQuery,
} from "./api";
import { Detail, Details, ObservedTime } from "./components";
import { usePageContinuation } from "./use-page-continuation";
import { useSessionExpiry } from "./use-session-expiry";

type TunnelIntent = {
  mutation: { requestId: string };
  serverId: string;
  expectedConnectionVersion: bigint;
};

const phaseLabels: Record<ServerTunnelPhase, string> = {
  [ServerTunnelPhase.UNSPECIFIED]: "Not provisioned",
  [ServerTunnelPhase.QUEUED]: "Queued",
  [ServerTunnelPhase.PREPARED]: "Host prepared",
  [ServerTunnelPhase.CREATING]: "Creating tunnel",
  [ServerTunnelPhase.FINALIZING]: "Verifying connector",
  [ServerTunnelPhase.READY]: "Provisioning verified",
  [ServerTunnelPhase.NEEDS_ATTENTION]: "Needs attention",
};

function provisioning(phase?: ServerTunnelPhase) {
  return (
    phase === ServerTunnelPhase.QUEUED ||
    phase === ServerTunnelPhase.PREPARED ||
    phase === ServerTunnelPhase.CREATING ||
    phase === ServerTunnelPhase.FINALIZING
  );
}

export function ServerTunnel({ server }: { server: Server }) {
  const router = useRouter();
  const expireSession = useSessionExpiry();
  const { capturePage, isMounted } = usePageContinuation();
  const [intent, setIntent] = useState<{
    request: TunnelIntent;
    admitted: boolean;
  }>();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const tunnelQuery = useSuspenseQuery({
    ...serverTunnelQuery(server.id),
    refetchInterval: (query) =>
      !query.state.error && provisioning(query.state.data?.tunnel?.phase)
        ? 15_000
        : false,
  });
  const connectionQuery = useSuspenseQuery(cloudflareIntegrationQuery());
  const tunnel = tunnelQuery.data.tunnel;
  const integration = connectionQuery.data.integration;
  const metadataError = !!tunnelQuery.error || !!connectionQuery.error;
  const recordedIntent: TunnelIntent | undefined =
    tunnel?.requestId &&
    tunnel.serverId === server.id &&
    tunnel.expectedConnectionVersion > 0n
      ? {
          mutation: { requestId: tunnel.requestId },
          serverId: tunnel.serverId,
          expectedConnectionVersion: tunnel.expectedConnectionVersion,
        }
      : undefined;
  const absent =
    tunnel?.phase === ServerTunnelPhase.UNSPECIFIED && !tunnel.requestId;
  const canProvision =
    !intent && absent && integration?.configured && integration.version > 0n;
  const canReconcile =
    tunnel?.phase === ServerTunnelPhase.NEEDS_ATTENTION && recordedIntent;
  const busy = pending || tunnelQuery.isFetching || connectionQuery.isFetching;

  async function refreshData() {
    const queries = [
      serverTunnelQuery(server.id),
      cloudflareIntegrationQuery(),
    ];
    await Promise.all(
      queries.map((query) =>
        queryClient.cancelQueries({ queryKey: query.queryKey }),
      ),
    );
    await Promise.all([
      queryClient.fetchQuery({ ...serverTunnelQuery(server.id), staleTime: 0 }),
      queryClient.fetchQuery({ ...cloudflareIntegrationQuery(), staleTime: 0 }),
    ]);
  }

  async function refresh() {
    if (pending || router.state.status === "pending") return;
    const isCurrent = capturePage();
    setPending(true);
    setFailure("");
    setNotice("");
    try {
      await refreshData();
    } catch (error) {
      if (isCurrent() && !(await expireSession(error)) && isCurrent())
        setFailure(
          "Tunnel status refresh failed. Refresh before provisioning or reconciling.",
        );
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  async function provision(request: TunnelIntent) {
    if (pending || metadataError || router.state.status === "pending") return;
    const isCurrent = capturePage();
    const admitted = !!recordedIntent || intent?.admitted === true;
    setIntent({ request, admitted });
    setPending(true);
    setFailure("");
    setNotice("");
    try {
      const result = await cloudflare.provisionServerTunnel(request);
      if (!result.tunnel?.requestId)
        throw new Error("Tunnel intent unavailable");
      if (isCurrent()) {
        setIntent({ request, admitted: true });
        setNotice(
          "Request recorded. Provisioning status below comes from the panel's latest observation.",
        );
      }
      // Shared observations remain stale even when the operator leaves this page.
      await queryClient.invalidateQueries({
        queryKey: serverTunnelQuery(server.id).queryKey,
      });
    } catch (error) {
      if (!isCurrent() || (await expireSession(error)) || !isCurrent()) return;
      if (mutationRejected(error)) {
        if (!admitted) setIntent(undefined);
        setFailure(
          error instanceof ConnectError && error.code === Code.Aborted
            ? "The Cloudflare connection changed. Refresh status, then review the connection before provisioning."
            : "Request refused. Check final host setup, the Cloudflare connection and any existing tunnel intent, then refresh status.",
        );
        await queryClient.invalidateQueries({
          queryKey: cloudflareIntegrationQuery().queryKey,
        });
        await queryClient.invalidateQueries({
          queryKey: serverTunnelQuery(server.id).queryKey,
        });
      } else {
        setFailure(
          "Request outcome unknown. Check the same request to recover its result; no new tunnel intent will be sent.",
        );
      }
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  return (
    <section aria-labelledby="server-tunnel-title" aria-busy={pending}>
      <h2 id="server-tunnel-title">Cloudflare Tunnel</h2>
      <p>
        Provision a connector for this server. This does not create DNS records,
        application routes or deploy applications.
      </p>
      {metadataError && (
        <p role="alert">
          Refresh failed. Tunnel or connection details may be out of date;
          refresh before continuing.
        </p>
      )}
      <Details>
        <Detail label="Provisioning status">
          {absent
            ? "Not provisioned"
            : tunnel && tunnel.phase !== ServerTunnelPhase.UNSPECIFIED
              ? (phaseLabels[tunnel.phase] ?? "Unknown")
              : "Unavailable"}
        </Detail>
        <Detail label="Last provisioning observation">
          <ObservedTime value={tunnel?.observedAt} />
        </Detail>
        <Detail label="Account ID">
          <code>
            {tunnel?.accountId || integration?.accountId || "Not connected"}
          </code>
        </Detail>
        <Detail label="Tunnel ID">
          <code>{tunnel?.tunnelId || "Not assigned"}</code>
        </Detail>
        {tunnel?.requestId && (
          <Detail label="Provisioning request ID">
            <code>{tunnel.requestId}</code>
          </Detail>
        )}
      </Details>
      {tunnel?.message && <p className="notice">{tunnel.message}</p>}
      <p className="muted">
        Provisioning status updates automatically while pending. It does not
        establish application health.
      </p>
      {absent && !integration?.configured && (
        <p className="action-link">
          <Link to="/settings">Connect Cloudflare in Settings</Link> before
          provisioning.
        </p>
      )}
      {absent && (
        <p className="action-link">
          Complete server setup before provisioning. Private access is checked
          before creating the tunnel.
        </p>
      )}
      <div className="form-actions action-link">
        {intent && !recordedIntent ? (
          <Button
            disabled={busy || metadataError}
            onClick={() => void provision(intent.request)}
          >
            Check provision request
          </Button>
        ) : canReconcile ? (
          <Button
            disabled={busy || metadataError}
            onClick={() => void provision(canReconcile)}
          >
            Reconcile existing tunnel
          </Button>
        ) : canProvision ? (
          <Button
            variant="primary"
            disabled={busy || metadataError}
            onClick={() =>
              void provision({
                mutation: { requestId: crypto.randomUUID() },
                serverId: server.id,
                expectedConnectionVersion: integration.version,
              })
            }
          >
            Provision tunnel
          </Button>
        ) : null}
        <Button
          variant="secondary"
          disabled={busy}
          onClick={() => void refresh()}
        >
          Refresh tunnel status
        </Button>
      </div>
      {failure && (
        <p className="form-error" role="alert">
          {failure}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {canReconcile && (
        <p className="muted">
          Reconciliation keeps the original request and connection version. It
          can resume a stopped observation job; an uncertain create is never
          repeated. Review the provider and target when status needs attention.
        </p>
      )}
    </section>
  );
}
