import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { TriangleAlertIcon } from "lucide-react";
import { Fragment, useState } from "react";
import type { FormEvent } from "react";
import { toast } from "sonner";

import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxValue,
  useComboboxAnchor,
} from "@/components/ui/combobox";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";

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
import { PendingButton } from "./components";
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
  const { capturePage } = usePageContinuation();
  // Held only until the next Cloudflare call, then cleared.
  const [token, setToken] = useState("");
  const [baseVersion, setBaseVersion] = useState(integration.version);
  const [accountId, setAccountId] = useState(integration.accountId);
  const [zones, setZones] = useState<Zone[]>(() =>
    integration.zones.map((zone) => ({ ...zone, eligible: true })),
  );
  const [selected, setSelected] = useState<string[]>(() =>
    integration.zones.map((zone) => zone.id),
  );
  const [truncated, setTruncated] = useState(false);
  const [listed, setListed] = useState(integration.zones.length > 0);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [errors, setErrors] = useState<{
    account?: string;
    token?: string;
    zones?: string;
  }>({});
  const fail = (title: string) => toast.error(title);
  const anchor = useComboboxAnchor();
  const stale = integration.version !== baseVersion;

  function clearToken() {
    setToken("");
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

  const refresh = useMutation({
    mutationFn: async () => {
      const isCurrent = capturePage();
      clearToken();
      try {
        await reload(isCurrent);
      } catch (error) {
        if (isCurrent() && !(await expireSession(error)) && isCurrent())
          fail("Couldn’t reload the saved connection. Try again.");
      }
    },
  });

  const listZones = useMutation({
    mutationFn: async () => {
      setErrors({});
      if (!/^[0-9a-f]{32}$/.test(accountId)) {
        setErrors({ account: "Enter the 32-character account ID first." });
        return;
      }
      if (!tokenPattern.test(token) || token.length > 4096) {
        setErrors({ token: "Paste the API token first." });
        return;
      }
      const isCurrent = capturePage();
      try {
        const result = await cloudflare.listCandidateZones({
          accountId,
          token,
        });
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
        if (!isCurrent() || (await expireSession(error)) || !isCurrent())
          return;
        fail(
          error instanceof ConnectError && error.code === Code.PermissionDenied
            ? error.rawMessage
            : "Couldn’t reach Cloudflare. Check the account ID and try again.",
        );
      }
    },
  });

  const save = useMutation({
    mutationFn: async () => {
      const request = {
        mutation: { requestId: crypto.randomUUID() },
        expectedVersion: baseVersion,
        accountId,
        zoneIds: [...selected],
        token,
      } satisfies Parameters<typeof cloudflare.connectAccount>[0];
      setErrors({});
      if (!tokenPattern.test(request.token) || request.token.length > 4096) {
        request.token = "";
        setErrors({ token: "Paste the API token (a single line)." });
        return;
      }
      if (!selected.length) {
        request.token = "";
        setErrors({ zones: "Choose at least one domain." });
        return;
      }
      clearToken();
      const isCurrent = capturePage();
      try {
        let result;
        try {
          result = await cloudflare.connectAccount(request);
        } finally {
          request.token = "";
        }
        if (!result.integration?.configured)
          throw new Error("Missing saved Cloudflare connection");
        if (isCurrent()) setNeedsRefresh(true);
        try {
          await reload(isCurrent);
          if (isCurrent()) onDone?.();
        } catch (refreshError) {
          if (
            isCurrent() &&
            !(await expireSession(refreshError)) &&
            isCurrent()
          )
            fail("Saved, but the page couldn’t reload. Refresh it.");
        }
      } catch (error) {
        if (!isCurrent() || (await expireSession(error)) || !isCurrent())
          return;
        if (error instanceof ConnectError && error.code === Code.Aborted) {
          setNeedsRefresh(true);
          fail(
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
              fail("The connection changed elsewhere. Reload this page.");
          }
        } else if (
          error instanceof ConnectError &&
          error.code === Code.PermissionDenied
        ) {
          // The server returns fixed, actionable Cloudflare guidance here.
          fail(`${error.rawMessage} Then paste the token again.`);
        } else if (mutationRejected(error)) {
          fail(
            "Cloudflare refused the connection. Check the token’s permissions and the chosen domains, then paste the token again.",
          );
        } else {
          setNeedsRefresh(true);
          fail(
            "We couldn’t confirm whether this saved. Reload the connection to check, then paste the token again if needed.",
          );
        }
      } finally {
        if (!isCurrent()) setNeedsRefresh(true);
      }
    },
  });

  const pending = refresh.isPending || save.isPending;
  const blocked = pending || needsRefresh || stale || metadataError;

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked || router.state.status === "pending") return;
    save.mutate();
  }

  const zoneLabel = (id: string) => {
    const zone = zones.find((candidate) => candidate.id === id);
    if (!zone) return id;
    return zone.eligible
      ? zone.name
      : `${zone.name} — can’t be used (${zone.reason ?? "not eligible"})`;
  };

  return (
    <form onSubmit={submit} aria-busy={pending} autoComplete="off">
      <FieldGroup>
        {stale && (
          <Alert>
            <TriangleAlertIcon />
            <AlertDescription>
              The connection changed since you opened this form.
            </AlertDescription>
            <AlertAction>
              <PendingButton
                size="sm"
                variant="outline"
                pending={refresh.isPending}
                onClick={() => refresh.mutate()}
              >
                Reload
              </PendingButton>
            </AlertAction>
          </Alert>
        )}
        <FieldSet disabled={blocked}>
          <FieldGroup>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field data-invalid={!!errors.account || undefined}>
                <FieldLabel htmlFor="cloudflare-account">Account ID</FieldLabel>
                <Input
                  id="cloudflare-account"
                  value={accountId}
                  aria-invalid={!!errors.account || undefined}
                  onChange={(event) => setAccountId(event.target.value.trim())}
                  required
                  minLength={32}
                  maxLength={32}
                  pattern="[0-9a-f]{32}"
                  spellCheck={false}
                />
                <FieldDescription>
                  On your domain’s overview page, under API.
                </FieldDescription>
                <FieldError>{errors.account}</FieldError>
              </Field>
              <Field data-invalid={!!errors.token || undefined}>
                <FieldLabel htmlFor="cloudflare-token">API token</FieldLabel>
                <Input
                  id="cloudflare-token"
                  type="password"
                  value={token}
                  aria-invalid={!!errors.token || undefined}
                  onChange={(event) => setToken(event.target.value)}
                  required
                  maxLength={4096}
                  spellCheck={false}
                />
                <FieldDescription>
                  Stored encrypted. Never shown again.
                </FieldDescription>
                <FieldError>{errors.token}</FieldError>
              </Field>
            </div>
            {listed && zones.length ? (
              <Field data-invalid={!!errors.zones || undefined}>
                <FieldLabel>Domains</FieldLabel>
                <Combobox
                  multiple
                  disabled={blocked}
                  items={zones.map((zone) => zone.id)}
                  value={selected}
                  onValueChange={(value) => setSelected(value as string[])}
                  itemToStringLabel={(id: string) => zoneLabel(id)}
                >
                  <ComboboxChips
                    ref={anchor}
                    aria-invalid={!!errors.zones || undefined}
                  >
                    <ComboboxValue>
                      {(values: string[]) => (
                        <Fragment>
                          {values.map((id) => (
                            <ComboboxChip key={id}>
                              {zoneLabel(id)}
                            </ComboboxChip>
                          ))}
                          <ComboboxChipsInput placeholder="Choose domains" />
                        </Fragment>
                      )}
                    </ComboboxValue>
                  </ComboboxChips>
                  <ComboboxContent anchor={anchor}>
                    <ComboboxEmpty>No matching domains.</ComboboxEmpty>
                    <ComboboxList>
                      {(id: string) => (
                        <ComboboxItem
                          key={id}
                          value={id}
                          disabled={
                            !zones.find((zone) => zone.id === id)?.eligible
                          }
                        >
                          {zoneLabel(id)}
                        </ComboboxItem>
                      )}
                    </ComboboxList>
                  </ComboboxContent>
                </Combobox>
                {truncated && (
                  <FieldDescription>
                    Only the first domains are shown. Restrict the token to the
                    domains you need.
                  </FieldDescription>
                )}
                <FieldError>{errors.zones}</FieldError>
              </Field>
            ) : (
              <FieldDescription>
                {listed
                  ? "The token can’t see any domains. Give it Zone → Read and DNS → Edit on your domain."
                  : "Paste the token, then load the domains it can manage."}
              </FieldDescription>
            )}
            <div className="flex">
              <PendingButton
                type="button"
                size="sm"
                variant="outline"
                pending={listZones.isPending}
                onClick={() => listZones.mutate()}
              >
                {listed ? "Reload domains" : "Load domains"}
              </PendingButton>
            </div>
          </FieldGroup>
        </FieldSet>
        <div className="flex flex-wrap gap-2">
          <PendingButton
            type="submit"
            pending={save.isPending}
            disabled={blocked || !selected.length}
          >
            {integration.configured ? "Save connection" : "Connect Cloudflare"}
          </PendingButton>
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
      </FieldGroup>
    </form>
  );
}
