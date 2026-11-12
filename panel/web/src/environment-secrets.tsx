import { KeyIcon, PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { useRef, useState } from "react";
import type { FormEvent } from "react";

import type { Environment, SecretValue } from "../../gen/ts/panel/v1/panel_pb";
import {
  environmentListKey,
  environmentQuery,
  mutationRejected,
  panel,
  queryClient,
  refreshEnvironment,
} from "./api";
import {
  Banner,
  Button,
  Card,
  Empty,
  EnvironmentStatus,
  PageHeader,
} from "./components";
import { secretChangeProblem } from "./secret-changes";
import { useSessionExpiry } from "./use-session-expiry";

const route = getRouteApi("/_private/environments/$environmentId/secrets");

export function EnvironmentSecretsRoute() {
  return <EnvironmentSecrets id={route.useParams().environmentId} />;
}

export function EnvironmentSecrets({ id }: { id: string }) {
  const { data, error } = useSuspenseQuery(environmentQuery(id));
  const environment = data.environment;
  if (!environment?.settings)
    return (
      <Card>
        <Empty title="Environment not found">
          <Link to="/projects" search={{ cursor: "" }}>
            Back to projects
          </Link>
        </Empty>
      </Card>
    );
  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link
            key="environment"
            to="/environments/$environmentId"
            params={{ environmentId: id }}
            search={{ services: "", releases: "" }}
          >
            {environment.settings.name}
          </Link>,
        ]}
        title="Secrets"
        badge={<EnvironmentStatus environment={environment} />}
        description="Environment variables your services read. Values are encrypted and never shown again."
      />
      <div className="stack" style={{ maxWidth: "48rem" }}>
        <SecretForm
          key={id}
          environment={environment}
          metadataError={!!error}
        />
      </div>
    </>
  );
}

function clearValues(form: HTMLFormElement) {
  for (const input of form.querySelectorAll<HTMLTextAreaElement>(
    "textarea[data-secret-value]",
  ))
    input.value = "";
}

function SecretForm({
  environment,
  metadataError,
}: {
  environment: Environment;
  metadataError: boolean;
}) {
  const expireSession = useSessionExpiry();
  const form = useRef<HTMLFormElement>(null);
  const [base, setBase] = useState(environment);
  const [rows, setRows] = useState<string[]>([]);
  const [pending, setPending] = useState(false);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const stale = environment.version !== base.version;
  const blocked = pending || needsRefresh || stale || metadataError;

  async function reload() {
    if (form.current) clearValues(form.current);
    const current = await refreshEnvironment(environment.id);
    form.current?.reset();
    setBase(current);
    setRows([]);
    setNeedsRefresh(false);
  }

  async function refresh() {
    if (pending) return;
    setPending(true);
    setFailure("");
    setNotice("");
    try {
      await reload();
      setNotice("Unsaved changes discarded.");
    } catch (error) {
      if (!(await expireSession(error)))
        setFailure("Couldn’t reload secrets. Try again.");
    } finally {
      setPending(false);
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    const submittedForm = event.currentTarget;
    const upserts: Pick<SecretValue, "name" | "value">[] = [];
    for (const row of rows) {
      const name = submittedForm.elements.namedItem(`name-${row}`);
      const value = submittedForm.elements.namedItem(`value-${row}`);
      if (
        !(name instanceof HTMLInputElement) ||
        !(value instanceof HTMLTextAreaElement)
      ) {
        clearValues(submittedForm);
        setFailure("The form couldn’t be read. Enter the values again.");
        return;
      }
      upserts.push({ name: name.value, value: value.value });
      value.value = "";
    }
    const removeNames = new FormData(submittedForm)
      .getAll("remove")
      .map(String);
    setFailure("");
    setNotice("");
    const problem = secretChangeProblem(upserts, removeNames, base.secretNames);
    if (problem) {
      setFailure(
        `${problem} For safety the values were cleared — enter them again.`,
      );
      return;
    }
    setPending(true);
    try {
      // Plaintext exists only in this request, never in React or Query state.
      let result;
      try {
        result = await panel.saveEnvironmentSecrets({
          mutation: { requestId: crypto.randomUUID() },
          environmentId: environment.id,
          expectedVersion: base.version,
          upserts,
          removeNames,
        });
      } finally {
        for (const item of upserts) item.value = "";
      }
      if (!result.environment) throw new Error("Missing saved environment");
      setNotice("Saved. Deploy to apply the new values.");
      setNeedsRefresh(true);
      try {
        await queryClient.invalidateQueries({
          queryKey: environmentListKey(environment.projectId),
        });
        await reload();
      } catch (refreshError) {
        if (!(await expireSession(refreshError)))
          setFailure("Saved, but the page couldn’t reload. Refresh it.");
      }
    } catch (error) {
      if (await expireSession(error)) return;
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        setNeedsRefresh(true);
        setFailure(
          "Secrets were changed elsewhere. Showing the latest — enter your values again.",
        );
        try {
          await reload();
        } catch (refreshError) {
          if (!(await expireSession(refreshError)))
            setFailure("Secrets were changed elsewhere. Reload the page.");
        }
      } else if (mutationRejected(error)) {
        setFailure(
          "The change was refused. Check the names and sizes, then enter the values again.",
        );
      } else {
        setNeedsRefresh(true);
        setFailure(
          "We couldn’t confirm the save. Reload to check, then re-enter any values still needed.",
        );
      }
    } finally {
      setPending(false);
    }
  }

  return (
    <>
      {stale && (
        <Banner
          tone="warning"
          actions={
            <Button size="sm" pending={pending} onClick={() => void refresh()}>
              Load latest
            </Button>
          }
        >
          Secrets changed since you opened this page.
        </Banner>
      )}
      {metadataError && (
        <Banner tone="warning">Couldn’t refresh. Reload before saving.</Banner>
      )}
      <form
        ref={form}
        className="form"
        onSubmit={(event) => void submit(event)}
        autoComplete="off"
        aria-busy={pending}
      >
        <fieldset disabled={blocked} className="stack">
          <Card
            title="Saved secrets"
            description="To change a value, add it again below with the same name."
            labelledBy="saved-secrets-title"
            flush
          >
            {base.secretNames.length ? (
              <ul className="list">
                {base.secretNames.map((name) => (
                  <li key={name}>
                    <label className="list-row removable">
                      <code>{name}</code>
                      <span className="spacer" />
                      <span className="checkbox subtle">
                        <input type="checkbox" name="remove" value={name} />
                        Remove
                      </span>
                    </label>
                  </li>
                ))}
              </ul>
            ) : (
              <Empty icon={<KeyIcon size={20} />} title="No secrets yet">
                Add the values your Compose file expects below.
              </Empty>
            )}
          </Card>
          <Card
            title="Add or replace"
            description="Values are visible while you type and cleared as soon as you save. An empty value is allowed."
            labelledBy="secret-changes-title"
          >
            {rows.map((row, index) => (
              <div
                className="repeat-row repeat-row-secret"
                key={row}
                role="group"
                aria-label={`Secret ${index + 1}`}
              >
                <div className="field">
                  <label htmlFor={`name-${row}`}>Name</label>
                  <input
                    id={`name-${row}`}
                    name={`name-${row}`}
                    required
                    maxLength={128}
                    pattern="[A-Za-z_][A-Za-z0-9_]*"
                    autoComplete="off"
                    spellCheck={false}
                    className="mono"
                    placeholder="API_TOKEN"
                  />
                </div>
                <div className="field">
                  <label htmlFor={`value-${row}`}>Value</label>
                  <textarea
                    id={`value-${row}`}
                    name={`value-${row}`}
                    data-secret-value=""
                    rows={1}
                    wrap="soft"
                    maxLength={16384}
                    autoComplete="off"
                    spellCheck={false}
                  />
                </div>
                <div className="button-cell">
                  <Button
                    type="button"
                    variant="ghost"
                    shape="square"
                    aria-label={`Discard secret ${index + 1}`}
                    icon={<TrashIcon size={16} />}
                    onClick={() =>
                      setRows(rows.filter((entry) => entry !== row))
                    }
                  />
                </div>
              </div>
            ))}
            <div>
              <Button
                type="button"
                icon={<PlusIcon size={16} />}
                disabled={rows.length >= 64}
                onClick={() => setRows([...rows, crypto.randomUUID()])}
              >
                Add secret
              </Button>
            </div>
          </Card>
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
            disabled={blocked && !pending}
          >
            Save secrets
          </Button>
          <Button
            type="button"
            variant="ghost"
            disabled={pending}
            onClick={() => void refresh()}
          >
            Discard changes
          </Button>
        </div>
      </form>
    </>
  );
}
