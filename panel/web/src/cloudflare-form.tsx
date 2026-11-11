import { Button } from "@cloudflare/kumo/components/button";
import { Code, ConnectError } from "@connectrpc/connect";
import { useRouter } from "@tanstack/react-router";
import { useRef, useState } from "react";
import type { FormEvent } from "react";

import type { CloudflareIntegration } from "../../gen/ts/panel/v1/cloudflare_pb";
import {
  cloudflare,
  cloudflareIntegrationQuery,
  mutationRejected,
  queryClient,
} from "./api";
import { usePageContinuation } from "./use-page-continuation";
import { useSessionExpiry } from "./use-session-expiry";

export function CloudflareForm({
  integration,
  metadataError,
}: {
  integration: CloudflareIntegration;
  metadataError: boolean;
}) {
  const router = useRouter();
  const expireSession = useSessionExpiry();
  const { capturePage, isMounted } = usePageContinuation();
  const form = useRef<HTMLFormElement>(null);
  const [baseVersion, setBaseVersion] = useState(integration.version);
  const [accountId, setAccountId] = useState(integration.accountId);
  const [zoneIds, setZoneIds] = useState(() =>
    integration.zones.length ? integration.zones.map((zone) => zone.id) : [""],
  );
  const [pending, setPending] = useState(false);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const stale = integration.version !== baseVersion;
  const blocked = pending || needsRefresh || stale || metadataError;

  function clearToken() {
    const input = form.current?.elements.namedItem("cloudflare-token");
    if (input instanceof HTMLInputElement) input.value = "";
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
    setZoneIds(
      result.integration.zones.length
        ? result.integration.zones.map((zone) => zone.id)
        : [""],
    );
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
      if (isCurrent())
        setNotice(
          "Current connection metadata loaded. Review the account and zones, then enter a token for any new change.",
        );
    } catch (error) {
      if (isCurrent() && !(await expireSession(error)) && isCurrent())
        setFailure(
          "Cloudflare status refresh failed. Refresh before saving again.",
        );
    } finally {
      if (isMounted()) setPending(false);
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
      zoneIds: [...zoneIds],
      token: input.value,
    } satisfies Parameters<typeof cloudflare.connectAccount>[0];
    input.value = "";
    setFailure("");
    setNotice("");
    if (
      !/^[A-Za-z0-9._~+/-]+=*$/.test(request.token) ||
      request.token.length > 4096
    ) {
      request.token = "";
      setFailure(
        "Enter a single-line account API token of at most 4096 ASCII characters.",
      );
      input.focus();
      return;
    }
    if (new Set(zoneIds).size !== zoneIds.length) {
      request.token = "";
      setFailure(
        "Zone IDs must be unique. The token was cleared; enter it again.",
      );
      return;
    }
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
        setNotice(
          `Connection version ${result.integration.version} saved. Only token validity and selected read access were checked.`,
        );
        setNeedsRefresh(true);
      }
      try {
        await reload(isCurrent);
      } catch (refreshError) {
        if (isCurrent() && !(await expireSession(refreshError)) && isCurrent())
          setFailure(
            "Saved, but metadata refresh failed. Refresh before entering another token.",
          );
      }
    } catch (error) {
      if (!isCurrent() || (await expireSession(error)) || !isCurrent()) return;
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        setNeedsRefresh(true);
        setFailure(
          "The connection changed elsewhere. Review refreshed account and zone details, then enter the token again.",
        );
        try {
          await reload(isCurrent);
        } catch (refreshError) {
          if (
            isCurrent() &&
            !(await expireSession(refreshError)) &&
            isCurrent()
          )
            setFailure(
              "The connection changed and refresh failed. Refresh before saving again.",
            );
        }
      } else if (mutationRejected(error)) {
        setFailure(
          "Connection was refused. Check the account token, its permissions and the selected active zones, then enter the token again.",
        );
      } else {
        setNeedsRefresh(true);
        setFailure(
          "Save outcome unknown. No token was retained for retry. Refresh metadata, review the account, zones and version, then re-enter any required change.",
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
      className="settings-form"
      onSubmit={(event) => void submit(event)}
      aria-busy={pending}
      autoComplete="off"
    >
      {stale && (
        <p role="alert">
          The connection changed since this form opened. Refresh metadata to
          discard this draft before saving.
        </p>
      )}
      <fieldset disabled={blocked} className="form-fields">
        <label htmlFor="cloudflare-account">Account ID</label>
        <input
          id="cloudflare-account"
          value={accountId}
          onChange={(event) => setAccountId(event.target.value)}
          required
          minLength={32}
          maxLength={32}
          pattern="[0-9a-f]{32}"
          spellCheck={false}
        />
        <label htmlFor="cloudflare-token">Account-owned API token</label>
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
        <p id="cloudflare-token-help" className="muted">
          The token is cleared before submission. It is never filled in from
          saved credentials.
        </p>
        <fieldset className="form-fields">
          <legend>Explicit zone IDs</legend>
          <p className="muted">
            Choose 1–32 active full public zones belonging to this account.
          </p>
          {zoneIds.map((zone, index) => (
            <div className="zone-input-row" key={index}>
              <label htmlFor={`cloudflare-zone-${index}`}>
                Zone ID {index + 1}
              </label>
              <input
                id={`cloudflare-zone-${index}`}
                value={zone}
                onChange={(event) =>
                  setZoneIds(
                    zoneIds.map((value, position) =>
                      position === index ? event.target.value : value,
                    ),
                  )
                }
                required
                minLength={32}
                maxLength={32}
                pattern="[0-9a-f]{32}"
                spellCheck={false}
              />
              <Button
                type="button"
                variant="secondary"
                disabled={zoneIds.length <= 1}
                aria-label={`Remove zone ${index + 1}`}
                onClick={() =>
                  setZoneIds(
                    zoneIds.filter((_, position) => position !== index),
                  )
                }
              >
                Remove
              </Button>
            </div>
          ))}
          <Button
            type="button"
            disabled={zoneIds.length >= 32}
            onClick={() => setZoneIds([...zoneIds, ""])}
          >
            Add zone ({zoneIds.length}/32)
          </Button>
        </fieldset>
      </fieldset>
      {failure && (
        <p className="form-error" role="alert">
          {failure}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      <p className="muted">
        Saving replaces the saved account, token and complete zone selection. It
        does not create tunnels, change DNS or apply routes.
      </p>
      <div className="form-actions">
        <Button type="submit" variant="primary" disabled={blocked}>
          {integration.configured
            ? "Replace Cloudflare connection"
            : "Save Cloudflare connection"}
        </Button>
        <Button
          type="button"
          variant="secondary"
          disabled={pending}
          onClick={() => void refresh()}
        >
          Discard draft and refresh status
        </Button>
      </div>
    </form>
  );
}
