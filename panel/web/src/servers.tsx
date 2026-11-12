import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ActivityIcon, ServerIcon, TriangleAlertIcon } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";

import { serverQuery, serversQuery } from "./api";
import {
  ButtonLink,
  type Crumb,
  Detail,
  Details,
  Id,
  ObservedTime,
  Page,
  Readiness,
  CursorPagination,
} from "./components";
import { ServerTunnel } from "./server-tunnel";

export function Servers({ cursor }: { cursor: string }) {
  const { data, error } = useSuspenseQuery({
    ...serversQuery(cursor),
    refetchInterval: 30_000,
  });
  const next = data.page?.nextPageToken ?? "";
  return (
    <Page title="Servers" description="Hosts managed privately over Tailscale.">
      {error && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription>
            Couldn’t refresh servers. Status may be out of date.
          </AlertDescription>
        </Alert>
      )}
      {data.servers.length ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.servers.map((server) => (
            <Card
              key={server.id}
              className="relative transition-colors hover:bg-muted/50 has-[a:focus-visible]:ring-2 has-[a:focus-visible]:ring-ring"
            >
              <CardHeader>
                <CardTitle className="flex min-w-0 items-center gap-2">
                  <ServerIcon
                    className="size-4 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                  {/* The title link covers the card; badges stay outside it. */}
                  <Link
                    className="truncate outline-none after:absolute after:inset-0"
                    to="/servers/$serverId"
                    params={{ serverId: server.id }}
                  >
                    {server.name}
                  </Link>
                </CardTitle>
                <CardAction>
                  <Readiness value={server.readiness} />
                </CardAction>
              </CardHeader>
              <CardContent className="flex flex-col gap-2 text-muted-foreground">
                <span className="font-mono text-xs">
                  {server.tailscaleAddress}
                </span>
                <ObservedTime value={server.observedAt} prefix="Checked" />
                {server.panelHost && (
                  <span>
                    <Badge variant="secondary">Panel host</Badge>
                  </span>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      ) : (
        <Empty className="border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <ServerIcon />
            </EmptyMedia>
            <EmptyTitle>No servers yet</EmptyTitle>
            <EmptyDescription>
              The panel’s own host appears here after panel setup.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      <CursorPagination
        previous={cursor ? { to: "/servers" } : undefined}
        next={next ? { to: "/servers", search: { cursor: next } } : undefined}
      />
    </Page>
  );
}

export function Server({ id }: { id: string }) {
  const { data, error } = useSuspenseQuery({
    ...serverQuery(id),
    refetchInterval: 30_000,
  });
  const server = data.server;
  const crumbs: Crumb[] = [{ label: "Servers", link: { to: "/servers" } }];
  if (!server)
    return (
      <Page title="Server not found" crumbs={crumbs}>
        <Empty className="border">
          <EmptyHeader>
            <EmptyTitle>Server not found</EmptyTitle>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink variant="outline" to="/servers">
              Back to servers
            </ButtonLink>
          </EmptyContent>
        </Empty>
      </Page>
    );

  return (
    <Page
      title={server.name}
      crumbs={crumbs}
      description={
        server.panelHost
          ? "Runs the panel itself, so it can’t be removed."
          : "Managed over private SSH through Tailscale."
      }
      actions={
        <ButtonLink variant="outline" to="/operations" search={{ server: id }}>
          <ActivityIcon data-icon="inline-start" />
          Activity
        </ButtonLink>
      }
    >
      {error && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription>
            Couldn’t refresh this server. Status may be out of date.
          </AlertDescription>
        </Alert>
      )}
      <div className="grid gap-4 md:grid-cols-2">
        <ServerTunnel key={server.id} server={server} />
        <Card>
          <CardHeader>
            <CardTitle>Details</CardTitle>
            <CardAction>
              <Readiness value={server.readiness} />
            </CardAction>
          </CardHeader>
          <CardContent>
            <Details>
              <Detail label="Tailscale address">
                <Id value={server.tailscaleAddress} />
              </Detail>
              <Detail label="Last checked">
                <ObservedTime value={server.observedAt} />
              </Detail>
              <Detail label="SSH host key">
                <Id value={server.sshHostKeyFingerprint} />
              </Detail>
              <Detail label="Server ID">
                <Id value={server.id} />
              </Detail>
            </Details>
          </CardContent>
        </Card>
      </div>
    </Page>
  );
}
