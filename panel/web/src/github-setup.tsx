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
import { Banner, Button } from "./components";
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
        setFailure("GitHub setup was refused. Check the owner and App name.");
      } else {
        setFailure(
          "We couldn’t confirm this step. Retry — it won’t create anything twice.",
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
    <div className="stack" aria-busy={pending}>
      {!registered && !destination && (
        <form className="form" onSubmit={register}>
          <p className="muted">
            Create a GitHub App for this panel. GitHub then asks which
            repositories it may read — choose only the ones you deploy.
          </p>
          <fieldset className="fields" disabled={pending || !!intent}>
            <div className="fields-2">
              <div className="field">
                <label htmlFor="github-owner-kind">Owner type</label>
                <select id="github-owner-kind" name="kind">
                  <option value="user">Personal account</option>
                  <option value="organization">Organization</option>
                </select>
              </div>
              <div className="field">
                <label htmlFor="github-owner">GitHub owner</label>
                <input
                  id="github-owner"
                  name="owner"
                  required
                  maxLength={100}
                  pattern={"[A-Za-z0-9][A-Za-z0-9\\-]*"}
                  autoComplete="off"
                  placeholder="your-username"
                />
              </div>
            </div>
            <div className="field">
              <label htmlFor="github-app-name">App name</label>
              <input
                id="github-app-name"
                name="name"
                required
                maxLength={100}
                aria-describedby="github-app-name-help"
                placeholder="your-username-panel"
              />
              <p className="help" id="github-app-name-help">
                Must be unique across GitHub.
              </p>
            </div>
          </fieldset>
          <div className="form-actions">
            <Button
              type="submit"
              variant="primary"
              pending={pending}
              disabled={!!intent}
            >
              Create GitHub App
            </Button>
          </div>
        </form>
      )}
      {registered && !destination && !intent && (
        <div className="form-actions">
          <Button
            variant={installed ? "secondary" : "primary"}
            pending={pending}
            onClick={() =>
              void prepare({
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
            tone="info"
            title="Ready to continue on GitHub"
            actions={
              <Button type="submit" variant="primary">
                Continue to GitHub
              </Button>
            }
          >
            GitHub creates the App, then asks where to install it. Continue
            within 15 minutes.
          </Banner>
        </form>
      )}
      {destination?.kind === "installation" && (
        <Banner
          tone="info"
          title="Ready to continue on GitHub"
          actions={
            <a href={destination.href} rel="noreferrer">
              Continue to GitHub
            </a>
          }
        >
          Choose “Only select repositories” and pick the ones you deploy.
        </Banner>
      )}
      {failure && (
        <p className="field-error" role="alert">
          {failure}
        </p>
      )}
      {(intent || destination) && (
        <div className="form-actions">
          {intent && (
            <Button pending={pending} onClick={() => void prepare(intent)}>
              Retry
            </Button>
          )}
          <Button
            variant="ghost"
            disabled={pending}
            onClick={() => void refresh()}
          >
            I’m done on GitHub
          </Button>
        </div>
      )}
    </div>
  );
}
