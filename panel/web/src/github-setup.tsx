import {
  Banner,
  Button,
  Grid,
  Input,
  LinkButton,
  Select,
  Text,
} from "@cloudflare/kumo";
import { InfoIcon } from "@phosphor-icons/react";
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent, ReactNode } from "react";

import {
  errorMessage,
  github,
  integrationQuery,
  mutationRejected,
  queryClient,
} from "./api";
import { githubDestination, githubRegistration } from "./github-navigation";
import { useSessionExpiry } from "./use-session-expiry";

type Intent =
  | { kind: "registration"; request: Parameters<typeof github.beginSetup>[0] }
  | {
      kind: "installation";
      request: Parameters<typeof github.beginInstallation>[0];
    };
type Destination =
  | { kind: "registration"; action: string; manifestJson: string }
  | { kind: "installation"; href: string };

export function GitHubSetup({
  registered,
  installed,
  actions,
}: {
  registered: boolean;
  installed: boolean;
  /** Extra actions shown next to the installation button. */
  actions?: ReactNode;
}) {
  const expireSession = useSessionExpiry();
  const [intent, setIntent] = useState<Intent>();
  const [destination, setDestination] = useState<Destination>();
  const [failure, setFailure] = useState("");
  const [ownerKind, setOwnerKind] = useState("user");
  const preparation = useMutation({ mutationFn: prepare });
  const refreshing = useMutation({ mutationFn: refresh });
  const pending = preparation.isPending || refreshing.isPending;

  async function prepare(next: Intent) {
    setIntent(next);
    setFailure("");
    try {
      if (next.kind === "registration") {
        const result = await github.beginSetup(next.request);
        setDestination({
          kind: "registration",
          ...githubRegistration(
            result.actionUrl,
            result.manifestJson,
            window.location.origin,
          ),
        });
      } else {
        const result = await github.beginInstallation(next.request);
        setDestination({
          kind: "installation",
          href: githubDestination(result.installationUrl, "installation"),
        });
      }
      setIntent(undefined);
    } catch (error) {
      if (await expireSession(error)) return;
      if (mutationRejected(error)) {
        setIntent(undefined);
        setFailure("GitHub setup was refused. Check the owner and App name.");
      } else {
        setFailure(
          "We couldn’t confirm this step. Retry — it won’t create anything twice.",
        );
      }
    }
  }

  function register(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || intent) return;
    const values = new FormData(event.currentTarget);
    preparation.mutate({
      kind: "registration",
      request: {
        mutation: { requestId: crypto.randomUUID() },
        owner: String(values.get("owner") ?? ""),
        organization: ownerKind === "organization",
        name: String(values.get("name") ?? ""),
      },
    });
  }

  async function refresh() {
    setFailure("");
    try {
      await queryClient.cancelQueries({
        queryKey: integrationQuery().queryKey,
      });
      await queryClient.fetchQuery({ ...integrationQuery(), staleTime: 0 });
      setIntent(undefined);
      setDestination(undefined);
    } catch (error) {
      if (!(await expireSession(error))) setFailure(errorMessage(error));
    }
  }

  return (
    <div className="flex flex-col gap-4" aria-busy={pending}>
      {!registered && !destination && (
        <form className="flex flex-col gap-4" onSubmit={register}>
          <Text variant="secondary">
            Create a GitHub App for this panel. GitHub then asks which
            repositories it may read — choose only the ones you deploy.
          </Text>
          <fieldset
            className="flex flex-col gap-4"
            disabled={pending || !!intent}
          >
            <Grid variant="2up" gap="sm">
              <Select
                label="Owner type"
                value={ownerKind}
                disabled={pending || !!intent}
                onValueChange={(value) => setOwnerKind(value ?? "user")}
                items={{
                  user: "Personal account",
                  organization: "Organization",
                }}
              />
              <Input
                label="GitHub owner"
                name="owner"
                required
                maxLength={100}
                pattern={"[A-Za-z0-9][A-Za-z0-9\\-]*"}
                autoComplete="off"
                placeholder="your-username"
              />
            </Grid>
            <Input
              label="App name"
              name="name"
              required
              maxLength={100}
              description="Must be unique across GitHub."
              placeholder="your-username-panel"
            />
          </fieldset>
          <div>
            <Button
              type="submit"
              variant="primary"
              loading={preparation.isPending}
              disabled={!!intent || refreshing.isPending}
            >
              Create GitHub App
            </Button>
          </div>
        </form>
      )}
      {registered && !destination && !intent && (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant={installed ? "secondary" : "primary"}
            loading={preparation.isPending}
            onClick={() =>
              preparation.mutate({
                kind: "installation",
                request: { mutation: { requestId: crypto.randomUUID() } },
              })
            }
          >
            {installed ? "Change access on GitHub" : "Install on GitHub"}
          </Button>
          {actions}
        </div>
      )}
      {destination?.kind === "registration" && (
        <form method="post" action={destination.action} rel="noreferrer">
          <input
            type="hidden"
            name="manifest"
            value={destination.manifestJson}
          />
          <Banner
            icon={<InfoIcon weight="fill" />}
            title="Ready to continue on GitHub"
            description="GitHub creates the App, then asks where to install it. Continue within 15 minutes."
            action={
              <Button type="submit" variant="primary">
                Continue to GitHub
              </Button>
            }
          />
        </form>
      )}
      {destination?.kind === "installation" && (
        <Banner
          icon={<InfoIcon weight="fill" />}
          title="Ready to continue on GitHub"
          description="Choose “Only select repositories” and pick the ones you deploy."
          action={
            <LinkButton
              variant="primary"
              href={destination.href}
              rel="noreferrer"
            >
              Continue to GitHub
            </LinkButton>
          }
        />
      )}
      {failure && (
        <Text variant="error" role="alert">
          {failure}
        </Text>
      )}
      {(intent || destination) && (
        <div className="flex flex-wrap items-center gap-2">
          {intent && (
            <Button
              loading={preparation.isPending}
              disabled={refreshing.isPending}
              onClick={() => preparation.mutate(intent)}
            >
              Retry
            </Button>
          )}
          <Button
            variant="ghost"
            loading={refreshing.isPending}
            disabled={preparation.isPending}
            onClick={() => refreshing.mutate()}
          >
            I’m done on GitHub
          </Button>
        </div>
      )}
    </div>
  );
}
