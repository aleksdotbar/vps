import { Button } from "@cloudflare/kumo/components/button";
import { useState } from "react";
import type { FormEvent } from "react";

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
}: {
  registered: boolean;
  installed: boolean;
}) {
  const expireSession = useSessionExpiry();
  const [intent, setIntent] = useState<Intent>();
  const [destination, setDestination] = useState<Destination>();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState("");

  async function prepare(next: Intent) {
    if (pending) return;
    setPending(true);
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
        setFailure(
          "Setup was refused. Check the owner and App name, or refresh connection status before trying again.",
        );
      } else {
        setFailure(
          "Setup preparation outcome is unknown. Retry the same request to recover its result. No GitHub page has been opened.",
        );
      }
    } finally {
      setPending(false);
    }
  }

  function register(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || intent) return;
    const values = new FormData(event.currentTarget);
    void prepare({
      kind: "registration",
      request: {
        mutation: { requestId: crypto.randomUUID() },
        owner: String(values.get("owner") ?? ""),
        organization: values.get("kind") === "organization",
        name: String(values.get("name") ?? ""),
      },
    });
  }

  async function refresh() {
    if (pending) return;
    setPending(true);
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
    } finally {
      setPending(false);
    }
  }

  return (
    <div aria-busy={pending}>
      <p>
        {registered
          ? "Install this App on its owner account using Only select repositories. Keep contents: read and actions: write permissions. Select at most 100 repositories."
          : "Register an App owned by your GitHub user or organization, then install it using Only select repositories. The panel requests contents: read and actions: write permissions."}
      </p>
      {!registered && !destination && (
        <form className="settings-form" onSubmit={register}>
          <label htmlFor="github-owner-kind">Owner type</label>
          <select
            id="github-owner-kind"
            name="kind"
            disabled={pending || !!intent}
          >
            <option value="user">Personal account</option>
            <option value="organization">Organization</option>
          </select>
          <label htmlFor="github-owner">GitHub owner</label>
          <input
            id="github-owner"
            name="owner"
            required
            maxLength={100}
            pattern={"[A-Za-z0-9][A-Za-z0-9\\-]*"}
            readOnly={pending || !!intent}
            autoComplete="off"
          />
          <label htmlFor="github-app-name">App name</label>
          <input
            id="github-app-name"
            name="name"
            required
            maxLength={100}
            readOnly={pending || !!intent}
          />
          <Button
            type="submit"
            variant="primary"
            disabled={pending || !!intent}
          >
            Prepare App registration
          </Button>
        </form>
      )}
      {registered && !destination && !intent && (
        <Button
          disabled={pending}
          onClick={() =>
            void prepare({
              kind: "installation",
              request: { mutation: { requestId: crypto.randomUUID() } },
            })
          }
        >
          {installed
            ? "Update repository access on GitHub"
            : "Prepare installation"}
        </Button>
      )}
      {destination?.kind === "registration" && (
        <form
          className="settings-form"
          method="post"
          action={destination.action}
          rel="noreferrer"
        >
          <input
            type="hidden"
            name="manifest"
            value={destination.manifestJson}
          />
          <p>
            Continue within 15 minutes in this browser session. GitHub will ask
            you to register and install the App.
          </p>
          <Button type="submit" variant="primary">
            Continue to GitHub
          </Button>
        </form>
      )}
      {destination?.kind === "installation" && (
        <p className="action-link">
          <a href={destination.href} rel="noreferrer">
            Continue installation on GitHub
          </a>
        </p>
      )}
      {failure && (
        <p className="form-error" role="alert">
          {failure}
        </p>
      )}
      <div className="form-actions">
        {intent && (
          <Button disabled={pending} onClick={() => void prepare(intent)}>
            Retry same preparation
          </Button>
        )}
        <Button
          variant="secondary"
          disabled={pending}
          onClick={() => void refresh()}
        >
          Refresh connection status
        </Button>
      </div>
    </div>
  );
}
