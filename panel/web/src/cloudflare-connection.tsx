import { Button } from "@cloudflare/kumo/components/button";
import { useSuspenseQuery } from "@tanstack/react-query";

import { cloudflareIntegrationQuery } from "./api";
import { CloudflareForm } from "./cloudflare-form";
import { Detail, Details, ObservedTime } from "./components";

export function CloudflareConnection() {
  const { data, error, refetch, isFetching } = useSuspenseQuery(
    cloudflareIntegrationQuery(),
  );
  const integration = data.integration;
  return (
    <section aria-labelledby="cloudflare-title">
      <h2 id="cloudflare-title">Cloudflare</h2>
      {!integration ? (
        <>
          <p role="alert">Cloudflare connection details are unavailable.</p>
          <Button
            variant="secondary"
            disabled={isFetching}
            onClick={() => void refetch()}
          >
            Refresh Cloudflare status
          </Button>
        </>
      ) : (
        <>
          <p>
            Use an account-owned API token with Cloudflare One Connector:
            cloudflared Write for this account and DNS Write restricted to the
            selected zones.{" "}
            <a
              href="https://developers.cloudflare.com/fundamentals/api/get-started/account-owned-tokens/"
              target="_blank"
              rel="noreferrer"
            >
              Token setup instructions
            </a>
          </p>
          {error && (
            <p role="alert">
              Cloudflare refresh failed. Details may be out of date; refresh
              before saving.
            </p>
          )}
          <Details>
            <Detail label="Connection">
              {integration.configured ? "Saved" : "Not connected"}
            </Detail>
            <Detail label="Current version">
              {integration.configured ? String(integration.version) : "None"}
            </Detail>
            <Detail label="Account ID">
              <code>{integration.accountId || "Not configured"}</code>
            </Detail>
            <Detail label="Token ID">
              <code>{integration.tokenId || "Not configured"}</code>
            </Detail>
            <Detail label="Last read verification">
              <ObservedTime value={integration.verifiedAt} />
            </Detail>
            <Detail label="Token expiry">
              {integration.expiresAt ? (
                <ObservedTime value={integration.expiresAt} />
              ) : (
                "Not reported"
              )}
            </Detail>
          </Details>
          {integration.zones.length ? (
            <ul className="resource-list" aria-label="Saved Cloudflare zones">
              {integration.zones.map((zone) => (
                <li key={zone.id}>
                  <span>{zone.name}</span>
                  <code>{zone.id}</code>
                </li>
              ))}
            </ul>
          ) : (
            <p className="muted">No zones configured.</p>
          )}
          <p className="action-link">
            Read verification does not prove write permissions or tunnel, DNS or
            route readiness. Saving this connection does not deploy
            applications.
          </p>
          <CloudflareForm integration={integration} metadataError={!!error} />
        </>
      )}
    </section>
  );
}
