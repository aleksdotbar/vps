import { Code, ConnectError } from "@connectrpc/connect";
import { useRouter } from "@tanstack/react-router";
import { useRef, useState } from "react";
import type { FormEvent } from "react";

import type {
  CandidateZone,
  CloudflareIntegration,
} from "../../gen/ts/panel/v1/cloudflare_pb";
import {
  cloudflare,
  cloudflareIntegrationQuery,
  mutationRejected,
  queryClient,
} from "./api";
import { Banner, Button, Empty } from "./components";
import { usePageContinuation } from "./use-page-continuation";
import { useSessionExpiry } from "./use-session-expiry";

const tokenPattern = /^[A-Za-z0-9._~+/-]+=*$/;

type Zone = { id: string; name: string; eligible: boolean; reason?: string };

function zoneReason(zone: CandidateZone) {
  if (zone.paused) return "paused";
  if (zone.status !== "active") return zone.status || "not active";
  if (zone.type !== "full") return `${zone.type || "partial"} setup`;
  return undefined;
}

export function CloudflareForm({
  integration,
  metadataError,
  onDone,
}: {
  integration: CloudflareIntegration;
  metadataError: boolean;
  onDone?: () => void;
}) {
  const router = useRouter();
  const expireSession = useSessionExpiry();
  const { capturePage, isMounted } = usePageContinuation();
  const form = useRef<HTMLFormElement>(null);
  const [baseVersion, setBaseVersion] = useState(integration.version);
  const [accountId, setAccountId] = useState(integration.accountId);
  const [zones, setZones] = useState<Zone[]>(() =>
    integration.zones.map((zone) => ({ ...zone, eligible: true })),
  );
  const [selected, setSelected] = useState<string[]>(() =>
    integration.zones.map((zone) => zone.id),
  );
  const [truncated, setTruncated] = useState(false);
  const [listing, setListing] = useState(false);
  const [listed, setListed] = useState(integration.zones.length > 0);
  const [pending, setPending] = useState(false);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const stale = integration.version !== baseVersion;
  const blocked = pending || needsRefresh || stale || metadataError;

  function tokenInput() {
    const input = form.current?.elements.namedItem("cloudflare-token");
    return input instanceof HTMLInputElement ? input : undefined;
  }

  function clearToken() {
    const input = tokenInput();
    if (input) input.value = "";
  }

  async function reload(isCurrent: () => boolean) {
    await queryClient.cancelQueries({
      queryKey: cloudflareIntegrationQuery().queryKey,
    });
    const result = await queryClient.fetchQuery({
      ...cloudflareIntegrationQuery(),
      staleTime: 0,
    });
    if (!result.integration) throw new Error("Cloudflare metadata unavailable");
    if (!isCurrent()) return;
    clearToken();
    setBaseVersion(result.integration.version);
    setAccountId(result.integration.accountId);
    setZones(
      result.integration.zones.map((zone) => ({ ...zone, eligible: true })),
    );
    setSelected(result.integration.zones.map((zone) => zone.id));
    setListed(result.integration.zones.length > 0);
    setNeedsRefresh(false);
  }

  async function refresh() {
    if (pending || router.state.status === "pending") return;
    const isCurrent = capturePage();
    clearToken();
    setPending(true);
    setFailure("");
    setNotice("");
    try {
      await reload(isCurrent);
    } catch (error) {
      if (isCurrent() && !(await expireSession(error)) && isCurrent())
        setFailure("Couldn’t reload the saved connection. Try again.");
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  async function findZones() {
    if (listing || blocked) return;
    const input = tokenInput();
    const token = input?.value ?? "";
    setFailure("");
    if (!/^[0-9a-f]{32}$/.test(accountId)) {
      setFailure("Enter the 32-character account ID first.");
      return;
    }
    if (!tokenPattern.test(token) || token.length > 4096) {
      setFailure("Paste the API token first.");
      input?.focus();
      return;
    }
    const isCurrent = capturePage();
    setListing(true);
    try {
      // The token is read from the field for this call only; never kept in state.
      const result = await cloudflare.listCandidateZones({ accountId, token });
      if (!isCurrent()) return;
      const next = result.zones.map((zone) => {
        const reason = zoneReason(zone);
        return {
          id: zone.id,
          name: zone.name,
          eligible: zone.eligible,
          reason,
        };
      });
      setZones(next);
      setTruncated(result.truncated);
      setListed(true);
      setSelected((current) => {
        const kept = current.filter((id) =>
          next.some((zone) => zone.id === id && zone.eligible),
        );
        const eligible = next.filter((zone) => zone.eligible);
        // A single usable domain is preselected; otherwise the operator chooses.
        const only = eligible.length === 1 ? eligible[0] : undefined;
        return kept.length || !only ? kept : [only.id];
      });
    } catch (error) {
      if (!isCurrent() || (await expireSession(error)) || !isCurrent()) return;
      setFailure(
        error instanceof ConnectError && error.code === Code.PermissionDenied
          ? error.rawMessage
          : "Couldn’t reach Cloudflare. Check the account ID and try again.",
      );
    } finally {
      if (isMounted()) setListing(false);
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked || router.state.status === "pending") return;
    const input = event.currentTarget.elements.namedItem("cloudflare-token");
    if (!(input instanceof HTMLInputElement)) return;
    const request = {
      mutation: { requestId: crypto.randomUUID() },
      expectedVersion: baseVersion,
      accountId,
      zoneIds: [...selected],
      token: input.value,
    } satisfies Parameters<typeof cloudflare.connectAccount>[0];
    setFailure("");
    setNotice("");
    if (!tokenPattern.test(request.token) || request.token.length > 4096) {
      request.token = "";
      setFailure("Paste the API token (a single line).");
      input.focus();
      return;
    }
    if (!selected.length) {
      request.token = "";
      setFailure("Choose at least one domain.");
      return;
    }
    input.value = "";
    const isCurrent = capturePage();
    setPending(true);
    try {
      let result;
      try {
        result = await cloudflare.connectAccount(request);
      } finally {
        request.token = "";
      }
      if (!result.integration?.configured)
        throw new Error("Missing saved Cloudflare connection");
      if (isCurrent()) {
        setNotice("Cloudflare connected.");
        setNeedsRefresh(true);
      }
      try {
        await reload(isCurrent);
        if (isCurrent()) onDone?.();
      } catch (refreshError) {
        if (isCurrent() && !(await expireSession(refreshError)) && isCurrent())
          setFailure("Saved, but the page couldn’t reload. Refresh it.");
      }
    } catch (error) {
      if (!isCurrent() || (await expireSession(error)) || !isCurrent()) return;
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        setNeedsRefresh(true);
        setFailure(
          "The connection was changed elsewhere. Review it, then paste the token again.",
        );
        try {
          await reload(isCurrent);
        } catch (refreshError) {
          if (
            isCurrent() &&
            !(await expireSession(refreshError)) &&
            isCurrent()
          )
            setFailure("The connection changed elsewhere. Reload this page.");
        }
      } else if (
        error instanceof ConnectError &&
        error.code === Code.PermissionDenied
      ) {
        // The server returns fixed, actionable Cloudflare guidance here.
        setFailure(`${error.rawMessage} Then paste the token again.`);
      } else if (mutationRejected(error)) {
        setFailure(
          "Cloudflare refused the connection. Check the token’s permissions and the chosen domains, then paste the token again.",
        );
      } else {
        setNeedsRefresh(true);
        setFailure(
          "We couldn’t confirm whether this saved. Reload the connection to check, then paste the token again if needed.",
        );
      }
    } finally {
      if (isMounted()) {
        setPending(false);
        if (!isCurrent()) setNeedsRefresh(true);
      }
    }
  }

  return (
    <form
      ref={form}
      className="form"
      onSubmit={(event) => void submit(event)}
      aria-busy={pending}
      autoComplete="off"
    >
      {stale && (
        <Banner
          tone="warning"
          actions={
            <Button size="sm" onClick={() => void refresh()} pending={pending}>
              Reload
            </Button>
          }
        >
          The connection changed since you opened this form.
        </Banner>
      )}
      <fieldset disabled={blocked} className="fields">
        <div className="fields-2">
          <div className="field">
            <label htmlFor="cloudflare-account">Account ID</label>
            <input
              id="cloudflare-account"
              value={accountId}
              onChange={(event) => setAccountId(event.target.value.trim())}
              required
              minLength={32}
              maxLength={32}
              pattern="[0-9a-f]{32}"
              spellCheck={false}
              className="mono"
              aria-describedby="cloudflare-account-help"
            />
            <p className="help" id="cloudflare-account-help">
              On your domain’s overview page, under API.
            </p>
          </div>
          <div className="field">
            <label htmlFor="cloudflare-token">API token</label>
            <input
              id="cloudflare-token"
              name="cloudflare-token"
              type="password"
              required
              maxLength={4096}
              autoComplete="off"
              spellCheck={false}
              aria-describedby="cloudflare-token-help"
            />
            <p className="help" id="cloudflare-token-help">
              Stored encrypted. Never shown again.
            </p>
          </div>
        </div>
        <div className="field">
          <span className="label" id="cloudflare-zones-label">
            Domains
          </span>
          {listed ? (
            zones.length ? (
              <div
                className="card"
                role="group"
                aria-labelledby="cloudflare-zones-label"
                style={{ boxShadow: "none" }}
              >
                <ul className="list">
                  {zones.map((zone) => (
                    <li key={zone.id}>
                      <label
                        className="checkbox list-row"
                        style={{
                          cursor: zone.eligible ? "pointer" : "default",
                        }}
                      >
                        <input
                          type="checkbox"
                          checked={selected.includes(zone.id)}
                          disabled={!zone.eligible}
                          onChange={(event) =>
                            setSelected(
                              event.target.checked
                                ? [...selected, zone.id]
                                : selected.filter((id) => id !== zone.id),
                            )
                          }
                        />
                        <span className="checkbox-text">
                          <span>{zone.name}</span>
                          {!zone.eligible && (
                            <span className="subtle">
                              Can’t be used ({zone.reason ?? "not eligible"})
                            </span>
                          )}
                        </span>
                      </label>
                    </li>
                  ))}
                </ul>
              </div>
            ) : (
              <div className="card" style={{ boxShadow: "none" }}>
                <Empty title="No domains found">
                  The token can’t see any domains. Give it Zone → Read and DNS →
                  Edit on your domain.
                </Empty>
              </div>
            )
          ) : (
            <p className="help">
              Paste the token, then load the domains it can manage.
            </p>
          )}
          {truncated && (
            <p className="help">
              Only the first domains are shown. Restrict the token to the
              domains you need.
            </p>
          )}
          <div>
            <Button
              type="button"
              size="sm"
              pending={listing}
              onClick={() => void findZones()}
            >
              {listed ? "Reload domains" : "Load domains"}
            </Button>
          </div>
        </div>
      </fieldset>
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
      <div className="form-actions">
        <Button
          type="submit"
          variant="primary"
          pending={pending}
          disabled={blocked || !selected.length}
        >
          {integration.configured ? "Save connection" : "Connect Cloudflare"}
        </Button>
        {onDone && (
          <Button
            type="button"
            variant="ghost"
            disabled={pending}
            onClick={() => {
              clearToken();
              onDone();
            }}
          >
            Cancel
          </Button>
        )}
      </div>
    </form>
  );
}
