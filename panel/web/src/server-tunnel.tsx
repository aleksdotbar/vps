import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { InfoIcon, RefreshCwIcon, TriangleAlertIcon } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

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
  ButtonLink,
  Detail,
  Details,
  Id,
  ObservedTime,
  PendingButton,
  StatusBadge,
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
  [ServerTunnelPhase.QUEUED]: ["Queued", "pending"],
  [ServerTunnelPhase.PREPARED]: ["Preparing host", "pending"],
  [ServerTunnelPhase.CREATING]: ["Creating tunnel", "pending"],
  [ServerTunnelPhase.FINALIZING]: ["Checking connector", "pending"],
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
  const fail = (title: string) => toast.error(title);
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
      <PendingButton
        pending={provision.isPending}
        disabled={busy || metadataError}
        onClick={() => start(intent.request)}
      >
        Check request
      </PendingButton>
    ) : canReconcile ? (
      <PendingButton
        pending={provision.isPending}
        disabled={busy || metadataError}
        onClick={() => start(canReconcile)}
      >
        Resume setup
      </PendingButton>
    ) : canProvision ? (
      <PendingButton
        pending={provision.isPending}
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
      </PendingButton>
    ) : null;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          Cloudflare Tunnel
          <StatusBadge tone={phaseTone}>{phaseLabel}</StatusBadge>
        </CardTitle>
        <CardDescription>
          Carries public traffic for your domains to this server.
        </CardDescription>
        <CardAction>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Refresh tunnel status"
            disabled={busy}
            onClick={() => {
              if (router.state.status !== "pending") refresh.mutate();
            }}
          >
            <RefreshCwIcon
              className={refresh.isPending ? "animate-spin" : undefined}
            />
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {tunnel?.observedAt && (
          <p className="text-sm text-muted-foreground">
            <ObservedTime value={tunnel.observedAt} prefix="Checked" />
          </p>
        )}
        {metadataError && (
          <Alert>
            <TriangleAlertIcon />
            <AlertDescription>
              Couldn’t refresh the tunnel status. Refresh before continuing.
            </AlertDescription>
          </Alert>
        )}
        {tunnel?.message && (
          <Alert>
            <TriangleAlertIcon />
            <AlertDescription>{tunnel.message}</AlertDescription>
          </Alert>
        )}
        {absent && !integration?.configured && (
          <Alert>
            <InfoIcon />
            <AlertTitle>Connect Cloudflare first</AlertTitle>
            <AlertDescription>
              The tunnel is created in your Cloudflare account.
            </AlertDescription>
            <AlertAction>
              <ButtonLink
                size="sm"
                variant="outline"
                to="/settings"
                hash="cloudflare"
              >
                Open settings
              </ButtonLink>
            </AlertAction>
          </Alert>
        )}
        {canReconcile && (
          <p className="text-sm text-muted-foreground">
            Resuming reuses the original request, so a tunnel is never created
            twice.
          </p>
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
      </CardContent>
    </Card>
  );
}
