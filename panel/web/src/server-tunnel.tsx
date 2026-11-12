import {
  Badge,
  Banner,
  Button,
  LayerCard,
  RefreshButton,
  Text,
  useKumoToastManager,
} from "@cloudflare/kumo";
import { Code, ConnectError } from "@connectrpc/connect";
import { InfoIcon, WarningIcon } from "@phosphor-icons/react";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
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
import { ButtonLink, Detail, Details, Id, ObservedTime } from "./components";
import { usePageContinuation } from "./use-page-continuation";
import { useSessionExpiry } from "./use-session-expiry";

type TunnelIntent = {
  mutation: { requestId: string };
  serverId: string;
  expectedConnectionVersion: bigint;
};

type Tone = "neutral" | "info" | "success" | "warning";

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
  const { capturePage } = usePageContinuation();
  const [intent, setIntent] = useState<{
    request: TunnelIntent;
    admitted: boolean;
  }>();
  const toast = useKumoToastManager();
  const fail = (title: string) => toast.add({ title, variant: "error" });
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

  const refresh = useMutation({
    mutationFn: async () => {
      const isCurrent = capturePage();
      try {
        await refreshData();
      } catch (error) {
        if (isCurrent() && !(await expireSession(error)) && isCurrent())
          fail(
            "Couldn’t refresh the tunnel status. Try again before continuing.",
          );
      }
    },
  });

  const provision = useMutation({
    mutationFn: async (request: TunnelIntent) => {
      const isCurrent = capturePage();
      const admitted = !!recordedIntent || intent?.admitted === true;
      setIntent({ request, admitted });
      try {
        const result = await cloudflare.provisionServerTunnel(request);
        if (!result.tunnel?.requestId)
          throw new Error("Tunnel intent unavailable");
        if (isCurrent()) setIntent({ request, admitted: true });
        // Shared observations remain stale even when the operator leaves this page.
        await queryClient.invalidateQueries({
          queryKey: serverTunnelQuery(server.id).queryKey,
        });
      } catch (error) {
        if (!isCurrent() || (await expireSession(error)) || !isCurrent())
          return;
        if (mutationRejected(error)) {
          if (!admitted) setIntent(undefined);
          fail(
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
          fail(
            "We couldn’t confirm the request. Check its status — no second tunnel will be created.",
          );
        }
      }
    },
  });

  const busy = refresh.isPending || provision.isPending;

  function start(request: TunnelIntent) {
    if (busy || metadataError || router.state.status === "pending") return;
    provision.mutate(request);
  }

  const action =
    intent && !recordedIntent ? (
      <Button
        variant="primary"
        loading={provision.isPending}
        disabled={busy || metadataError}
        onClick={() => start(intent.request)}
      >
        Check request
      </Button>
    ) : canReconcile ? (
      <Button
        variant="primary"
        loading={provision.isPending}
        disabled={busy || metadataError}
        onClick={() => start(canReconcile)}
      >
        Resume setup
      </Button>
    ) : canProvision ? (
      <Button
        variant="primary"
        loading={provision.isPending}
        disabled={busy || metadataError}
        onClick={() =>
          start({
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
    <LayerCard>
      <LayerCard.Secondary className="flex items-center justify-between gap-2">
        Cloudflare Tunnel
        <RefreshButton
          aria-label="Refresh tunnel status"
          loading={refresh.isPending}
          disabled={busy}
          onClick={() => {
            if (router.state.status !== "pending") refresh.mutate();
          }}
        />
      </LayerCard.Secondary>
      <LayerCard.Primary className="flex flex-col gap-4">
        <Text variant="secondary">
          Carries public traffic for your domains to this server.
        </Text>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={phaseTone}>{phaseLabel}</Badge>
          {tunnel?.observedAt && (
            <Text as="span" variant="secondary" size="sm">
              <ObservedTime value={tunnel.observedAt} prefix="Checked" />
            </Text>
          )}
        </div>
        {metadataError && (
          <Banner
            variant="alert"
            icon={<WarningIcon weight="fill" />}
            description="Couldn’t refresh the tunnel status. Refresh before continuing."
          />
        )}
        {tunnel?.message && (
          <Banner
            variant="alert"
            icon={<WarningIcon weight="fill" />}
            description={tunnel.message}
          />
        )}
        {absent && !integration?.configured && (
          <Banner
            icon={<InfoIcon weight="fill" />}
            title="Connect Cloudflare first"
            description="The tunnel is created in your Cloudflare account."
            action={
              <ButtonLink size="sm" to="/settings" hash="cloudflare">
                Open settings
              </ButtonLink>
            }
          />
        )}
        {canReconcile && (
          <Text variant="secondary" size="sm">
            Resuming reuses the original request, so a tunnel is never created
            twice.
          </Text>
        )}
        {(tunnel?.tunnelId || tunnel?.accountId) && (
          <Details>
            <Detail label="Tunnel ID">
              <Id value={tunnel.tunnelId} />
            </Detail>
            <Detail label="Account">
              <Id value={tunnel.accountId || integration?.accountId || ""} />
            </Detail>
          </Details>
        )}
        {action && <div className="flex gap-2">{action}</div>}
      </LayerCard.Primary>
    </LayerCard>
  );
}
