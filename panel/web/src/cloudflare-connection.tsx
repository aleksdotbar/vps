import { ArrowSquareOutIcon, CloudIcon } from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";

import { cloudflareIntegrationQuery } from "./api";
import { CloudflareForm } from "./cloudflare-form";
import {
  Badge,
  Banner,
  Button,
  Card,
  Detail,
  Details,
  Empty,
  Id,
  ObservedTime,
  RefreshButton,
} from "./components";

export function CloudflareConnection() {
  const { data, error, refetch } = useSuspenseQuery(
    cloudflareIntegrationQuery(),
  );
  const integration = data.integration;
  const [editing, setEditing] = useState(false);
  const configured = !!integration?.configured;

  return (
    <Card
      title={
        <span className="row" id="cloudflare">
          <CloudIcon size={18} aria-hidden="true" />
          Cloudflare
          {configured ? (
            <Badge tone="success">Connected</Badge>
          ) : (
            <Badge>Not connected</Badge>
          )}
        </span>
      }
      description="Publishes your domains through a Cloudflare Tunnel."
      labelledBy="cloudflare-title"
      actions={
        <RefreshButton
          label="Refresh Cloudflare status"
          onRefresh={() => refetch()}
        />
      }
    >
      {!integration ? (
        <Empty title="Connection details unavailable">
          Refresh to try again.
        </Empty>
      ) : (
        <>
          {error && (
            <Banner tone="warning">
              Couldn’t refresh the connection. Details may be out of date.
            </Banner>
          )}
          {configured && (
            <Details>
              <Detail label="Domains">
                <span className="row" style={{ gap: "0.375rem" }}>
                  {integration.zones.map((zone) => (
                    <span className="chip" key={zone.id} title={zone.id}>
                      {zone.name}
                    </span>
                  ))}
                </span>
              </Detail>
              <Detail label="Account">
                <Id value={integration.accountId} label="account ID" />
              </Detail>
              <Detail label="Token">
                <span className="row" style={{ gap: "0.5rem" }}>
                  <Id value={integration.tokenId} short label="token ID" />
                  <span className="subtle">
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
            <div className="form-actions">
              <Button onClick={() => setEditing(true)}>
                Replace token or domains
              </Button>
            </div>
          ) : (
            <>
              <p className="help">
                Create an <strong>account API token</strong> (Manage Account →
                Account API Tokens) with{" "}
                <em>Cloudflare One Connector: cloudflared → Edit</em> on the
                account, and <em>Zone → Read</em> plus <em>DNS → Edit</em> on
                your domain.{" "}
                <a
                  className="row"
                  style={{ display: "inline-flex", gap: "0.25rem" }}
                  href="https://developers.cloudflare.com/fundamentals/api/get-started/account-owned-tokens/"
                  target="_blank"
                  rel="noreferrer"
                >
                  Guide
                  <ArrowSquareOutIcon size={12} aria-hidden="true" />
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
    </Card>
  );
}
