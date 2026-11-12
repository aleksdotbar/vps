import { useMutation } from "@tanstack/react-query";
import { InfoIcon } from "lucide-react";
import { useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import {
  errorMessage,
  github,
  integrationQuery,
  mutationRejected,
  queryClient,
} from "./api";
import { PendingButton } from "./components";
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

const ownerKinds = [
  { value: "user", label: "Personal account" },
  { value: "organization", label: "Organization" },
];

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
  const [ownerKind, setOwnerKind] = useState("user");
  const preparation = useMutation({ mutationFn: prepare });
  const refreshing = useMutation({ mutationFn: refresh });
  const pending = preparation.isPending || refreshing.isPending;

  async function prepare(next: Intent) {
    setIntent(next);
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
        toast.error("GitHub setup was refused. Check the owner and App name.");
      } else {
        toast.error(
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
    try {
      await queryClient.cancelQueries({
        queryKey: integrationQuery().queryKey,
      });
      await queryClient.fetchQuery({ ...integrationQuery(), staleTime: 0 });
      setIntent(undefined);
      setDestination(undefined);
    } catch (error) {
      if (!(await expireSession(error))) toast.error(errorMessage(error));
    }
  }

  return (
    <div className="flex flex-col gap-4" aria-busy={pending}>
      {!registered && !destination && (
        <form onSubmit={register}>
          <FieldGroup>
            <p className="text-muted-foreground">
              Create a GitHub App for this panel. GitHub then asks which
              repositories it may read — choose only the ones you deploy.
            </p>
            <fieldset className="contents" disabled={pending || !!intent}>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field>
                  <FieldLabel htmlFor="github-owner-kind">
                    Owner type
                  </FieldLabel>
                  <Select
                    items={ownerKinds}
                    value={ownerKind}
                    disabled={pending || !!intent}
                    onValueChange={(value) => setOwnerKind(value ?? "user")}
                  >
                    <SelectTrigger id="github-owner-kind" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {ownerKinds.map((kind) => (
                        <SelectItem key={kind.value} value={kind.value}>
                          {kind.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field>
                  <FieldLabel htmlFor="github-owner">GitHub owner</FieldLabel>
                  <Input
                    id="github-owner"
                    name="owner"
                    required
                    maxLength={100}
                    pattern={"[A-Za-z0-9][A-Za-z0-9\\-]*"}
                    autoComplete="off"
                    placeholder="your-username"
                  />
                </Field>
              </div>
              <Field>
                <FieldLabel htmlFor="github-app-name">App name</FieldLabel>
                <Input
                  id="github-app-name"
                  name="name"
                  required
                  maxLength={100}
                  placeholder="your-username-panel"
                />
                <FieldDescription>
                  Must be unique across GitHub.
                </FieldDescription>
              </Field>
            </fieldset>
            <Field orientation="horizontal">
              <PendingButton
                type="submit"
                pending={preparation.isPending}
                disabled={!!intent || refreshing.isPending}
              >
                Create GitHub App
              </PendingButton>
            </Field>
          </FieldGroup>
        </form>
      )}
      {registered && !destination && !intent && (
        <div className="flex flex-wrap items-center gap-2">
          <PendingButton
            variant={installed ? "outline" : "default"}
            pending={preparation.isPending}
            onClick={() =>
              preparation.mutate({
                kind: "installation",
                request: { mutation: { requestId: crypto.randomUUID() } },
              })
            }
          >
            {installed ? "Change access on GitHub" : "Install on GitHub"}
          </PendingButton>
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
          <Alert>
            <InfoIcon />
            <AlertTitle>Ready to continue on GitHub</AlertTitle>
            <AlertDescription>
              <p>
                GitHub creates the App, then asks where to install it. Continue
                within 15 minutes.
              </p>
              <Button type="submit" className="mt-3">
                Continue to GitHub
              </Button>
            </AlertDescription>
          </Alert>
        </form>
      )}
      {destination?.kind === "installation" && (
        <Alert>
          <InfoIcon />
          <AlertTitle>Ready to continue on GitHub</AlertTitle>
          <AlertDescription>
            <p>
              Choose “Only select repositories” and pick the ones you deploy.
            </p>
            <Button
              nativeButton={false}
              render={<a href={destination.href} rel="noreferrer" />}
              className="mt-3"
            >
              Continue to GitHub
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {(intent || destination) && (
        <div className="flex flex-wrap items-center gap-2">
          {intent && (
            <PendingButton
              variant="outline"
              pending={preparation.isPending}
              disabled={refreshing.isPending}
              onClick={() => preparation.mutate(intent)}
            >
              Retry
            </PendingButton>
          )}
          <PendingButton
            variant="ghost"
            pending={refreshing.isPending}
            disabled={preparation.isPending}
            onClick={() => refreshing.mutate()}
          >
            I’m done on GitHub
          </PendingButton>
        </div>
      )}
    </div>
  );
}
