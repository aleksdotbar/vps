import {
  Badge,
  Banner,
  Button,
  Empty,
  LayerCard,
  Link,
  RefreshButton,
  Text,
} from "@cloudflare/kumo";
import { CloudIcon, WarningIcon } from "@phosphor-icons/react";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";

import { cloudflareIntegrationQuery } from "./api";
import { CloudflareForm } from "./cloudflare-form";
import { Detail, Details, Id, ObservedTime } from "./components";

export function CloudflareConnection() {
  const { data, error, refetch } = useSuspenseQuery(
    cloudflareIntegrationQuery(),
  );
  const refresh = useMutation({ mutationFn: () => refetch() });
  const integration = data.integration;
  const [editing, setEditing] = useState(false);
  const configured = !!integration?.configured;

  return (
    <LayerCard id="cloudflare">
      <LayerCard.Secondary className="flex items-center justify-between gap-2">
        <span className="flex items-center gap-2">
          <CloudIcon size={18} aria-hidden="true" />
          Cloudflare
          {configured ? (
            <Badge variant="success">Connected</Badge>
          ) : (
            <Badge variant="neutral">Not connected</Badge>
          )}
        </span>
        <RefreshButton
          aria-label="Refresh Cloudflare status"
          loading={refresh.isPending}
          onClick={() => refresh.mutate()}
        />
      </LayerCard.Secondary>
      <LayerCard.Primary className="flex flex-col gap-4">
        <Text variant="secondary">
          Publishes your domains through a Cloudflare Tunnel.
        </Text>
        {!integration ? (
          <Empty
            title="Connection details unavailable"
            description="Refresh to try again."
          />
        ) : (
          <>
            {error && (
              <Banner
                variant="alert"
                icon={<WarningIcon weight="fill" />}
                description="Couldn’t refresh the connection. Details may be out of date."
              />
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
                    <Text as="span" variant="secondary" size="sm">
                      {integration.expiresAt ? (
                        <ObservedTime
                          value={integration.expiresAt}
                          prefix="Expires"
                        />
                      ) : (
                        "No expiry"
                      )}
                    </Text>
                  </span>
                </Detail>
                <Detail label="Last verified">
                  <ObservedTime value={integration.verifiedAt} />
                </Detail>
              </Details>
            )}
            {configured && !editing ? (
              <div className="flex gap-2">
                <Button onClick={() => setEditing(true)}>
                  Replace token or domains
                </Button>
              </div>
            ) : (
              <>
                <Text variant="secondary" size="sm">
                  Create an <strong>account API token</strong> (Manage Account →
                  Account API Tokens) with{" "}
                  <em>Cloudflare One Connector: cloudflared → Edit</em> on the
                  account, and <em>Zone → Read</em> plus <em>DNS → Edit</em> on
                  your domain.{" "}
                  <Link
                    href="https://developers.cloudflare.com/fundamentals/api/get-started/account-owned-tokens/"
                    target="_blank"
                    rel="noreferrer"
                  >
                    Guide
                    <Link.ExternalIcon />
                  </Link>
                </Text>
                <CloudflareForm
                  integration={integration}
                  metadataError={!!error}
                  onDone={configured ? () => setEditing(false) : undefined}
                />
              </>
            )}
          </>
        )}
      </LayerCard.Primary>
    </LayerCard>
  );
}
