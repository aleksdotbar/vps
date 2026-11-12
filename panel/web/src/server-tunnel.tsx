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
import {
  Badge,
  Banner,
  Button,
  Card,
  Detail,
  Details,
  Id,
  ObservedTime,
  RefreshButton,
  type Tone,
} from "./components";
import { usePageContinuation } from "./use-page-continuation";
import { useSessionExpiry } from "./use-session-expiry";

type TunnelIntent = {
  mutation: { requestId: string };
  serverId: string;
  expectedConnectionVersion: bigint;
};

const phaseLabels: Record<ServerTunnelPhase, [string, Tone]> = {
  [ServerTunnelPhase.UNSPECIFIED]: ["Not set up", "neutral"],
  [ServerTunnelPhase.QUEUED]: ["Queued", "info"],
  [ServerTunnelPhase.PREPARED]: ["Preparing host", "info"],
  [ServerTunnelPhase.CREATING]: ["Creating tunnel", "info"],
  [ServerTunnelPhase.FINALIZING]: ["Checking connector", "info"],
  [ServerTunnelPhase.READY]: ["Connected", "success"],
  [ServerTunnelPhase.NEEDS_ATTENTION]: ["Needs attention", "warning"],
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
  const busy = pending;
  const [phaseLabel, phaseTone]: [string, Tone] = absent
    ? phaseLabels[ServerTunnelPhase.UNSPECIFIED]
    : tunnel
      ? (phaseLabels[tunnel.phase] ?? ["Unknown", "neutral"])
      : ["Unavailable", "neutral"];

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
          "Couldn’t refresh the tunnel status. Try again before continuing.",
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
        setNotice("Tunnel requested. Status updates here automatically.");
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
            ? "The Cloudflare connection changed. Review it in Settings, then try again."
            : "The tunnel couldn’t be created. Check that the server is ready and Cloudflare is connected.",
        );
        await queryClient.invalidateQueries({
          queryKey: cloudflareIntegrationQuery().queryKey,
        });
        await queryClient.invalidateQueries({
          queryKey: serverTunnelQuery(server.id).queryKey,
        });
      } else {
        setFailure(
          "We couldn’t confirm the request. Check its status — no second tunnel will be created.",
        );
      }
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  const action =
    intent && !recordedIntent ? (
      <Button
        variant="primary"
        pending={pending}
        disabled={busy || metadataError}
        onClick={() => void provision(intent.request)}
      >
        Check request
      </Button>
    ) : canReconcile ? (
      <Button
        variant="primary"
        pending={pending}
        disabled={busy || metadataError}
        onClick={() => void provision(canReconcile)}
      >
        Resume setup
      </Button>
    ) : canProvision ? (
      <Button
        variant="primary"
        pending={pending}
        disabled={busy || metadataError}
        onClick={() =>
          void provision({
            mutation: { requestId: crypto.randomUUID() },
            serverId: server.id,
            expectedConnectionVersion: integration.version,
          })
        }
      >
        Create tunnel
      </Button>
    ) : null;

  return (
    <Card
      title="Cloudflare Tunnel"
      description="Carries public traffic for your domains to this server."
      labelledBy="server-tunnel-title"
      actions={
        <RefreshButton
          label="Refresh tunnel status"
          onRefresh={() => refresh()}
        />
      }
      footer={action}
    >
      <div className="row">
        <Badge tone={phaseTone} active={provisioning(tunnel?.phase)}>
          {phaseLabel}
        </Badge>
        {tunnel?.observedAt && (
          <span className="subtle">
            <ObservedTime value={tunnel.observedAt} prefix="Checked" />
          </span>
        )}
      </div>
      {metadataError && (
        <Banner tone="warning">
          Couldn’t refresh the tunnel status. Refresh before continuing.
        </Banner>
      )}
      {tunnel?.message && <Banner tone="warning">{tunnel.message}</Banner>}
      {absent && !integration?.configured && (
        <Banner
          tone="info"
          title="Connect Cloudflare first"
          actions={
            <Link to="/settings" hash="cloudflare">
              Open settings
            </Link>
          }
        >
          The tunnel is created in your Cloudflare account.
        </Banner>
      )}
      {canReconcile && (
        <p className="help">
          Resuming reuses the original request, so a tunnel is never created
          twice.
        </p>
      )}
      {(tunnel?.tunnelId || tunnel?.accountId) && (
        <Details>
          <Detail label="Tunnel ID">
            <Id value={tunnel.tunnelId} label="tunnel ID" />
          </Detail>
          <Detail label="Account">
            <Id
              value={tunnel.accountId || integration?.accountId || ""}
              label="account ID"
            />
          </Detail>
        </Details>
      )}
      {failure && (
        <p className="field-error" role="alert">
          {failure}
        </p>
      )}
      {notice && (
        <p className="help" role="status">
          {notice}
        </p>
      )}
    </Card>
  );
}
