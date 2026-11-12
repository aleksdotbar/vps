import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import {
  CloudIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { useState } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";

import { cloudflareIntegrationQuery } from "./api";
import { CloudflareForm } from "./cloudflare-form";
import { Detail, Details, Id, ObservedTime, StatusBadge } from "./components";

export function CloudflareConnection() {
  const { data, error, refetch } = useSuspenseQuery(
    cloudflareIntegrationQuery(),
  );
  const refresh = useMutation({ mutationFn: () => refetch() });
  const integration = data.integration;
  const [editing, setEditing] = useState(false);
  const configured = !!integration?.configured;

  return (
    <Card id="cloudflare">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <CloudIcon className="size-4" aria-hidden="true" />
          Cloudflare
          {configured ? (
            <StatusBadge tone="success">Connected</StatusBadge>
          ) : (
            <StatusBadge tone="neutral">Not connected</StatusBadge>
          )}
        </CardTitle>
        <CardDescription>
          Publishes your domains through a Cloudflare Tunnel.
        </CardDescription>
        <CardAction>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Refresh Cloudflare status"
            disabled={refresh.isPending}
            onClick={() => refresh.mutate()}
          >
            <RefreshCwIcon
              className={refresh.isPending ? "animate-spin" : undefined}
            />
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {!integration ? (
          <Empty>
            <EmptyHeader>
              <EmptyTitle>Connection details unavailable</EmptyTitle>
              <EmptyDescription>Refresh to try again.</EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <>
            {error && (
              <Alert>
                <TriangleAlertIcon />
                <AlertDescription>
                  Couldn’t refresh the connection. Details may be out of date.
                </AlertDescription>
              </Alert>
            )}
            {configured && (
              <Details>
                <Detail label="Domains">
                  <span className="flex flex-wrap gap-1.5">
                    {integration.zones.map((zone) => (
                      <Badge variant="outline" key={zone.id}>
                        {zone.name}
                      </Badge>
                    ))}
                  </span>
                </Detail>
                <Detail label="Account">
                  <Id value={integration.accountId} />
                </Detail>
                <Detail label="Token">
                  <span className="flex flex-wrap items-center gap-2">
                    <Id value={integration.tokenId} short />
                    <span className="text-muted-foreground">
                      {integration.expiresAt ? (
                        <ObservedTime
                          value={integration.expiresAt}
                          prefix="Expires"
                        />
                      ) : (
                        "No expiry"
                      )}
                    </span>
                  </span>
                </Detail>
                <Detail label="Last verified">
                  <ObservedTime value={integration.verifiedAt} />
                </Detail>
              </Details>
            )}
            {configured && !editing ? (
              <div className="flex gap-2">
                <Button variant="outline" onClick={() => setEditing(true)}>
                  Replace token or domains
                </Button>
              </div>
            ) : (
              <>
                <p className="text-sm text-muted-foreground">
                  Create an <strong>account API token</strong> (Manage Account →
                  Account API Tokens) with{" "}
                  <em>Cloudflare One Connector: cloudflared → Edit</em> on the
                  account, and <em>Zone → Read</em> plus <em>DNS → Edit</em> on
                  your domain.{" "}
                  <a
                    href="https://developers.cloudflare.com/fundamentals/api/get-started/account-owned-tokens/"
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 text-foreground underline underline-offset-4"
                  >
                    Guide
                    <ExternalLinkIcon className="size-3" aria-hidden="true" />
                  </a>
                </p>
                <CloudflareForm
                  integration={integration}
                  metadataError={!!error}
                  onDone={configured ? () => setEditing(false) : undefined}
                />
              </>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
