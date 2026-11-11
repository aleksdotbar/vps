import { Button } from "@cloudflare/kumo/components/button";
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
import { Detail, Details, Empty, PageTitle } from "./components";
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
      <Empty title="Environment unavailable">
        Return to the project and refresh.
      </Empty>
    );
  return (
    <>
      <Link
        className="back-link"
        to="/environments/$environmentId"
        params={{ environmentId: id }}
        search={{ services: "", releases: "" }}
      >
        Environment details
      </Link>
      <PageTitle
        title={`${environment.settings.name} secrets`}
        description="Values are never returned by the panel."
      />
      <Details>
        <Detail label="Environment version">
          {String(environment.version)}
        </Detail>
        <Detail label="Secret version">
          {environment.latestSecretVersionId || "Unavailable"}
        </Detail>
        <Detail label="Saved names">
          {environment.secretNames.length
            ? environment.secretNames.join(", ")
            : "None configured"}
        </Detail>
        <Detail label="Configuration">
          {environment.pendingChanges
            ? "Changes pending"
            : environment.currentReleaseId
              ? "Applied"
              : "Not deployed"}
        </Detail>
      </Details>
      <SecretForm key={id} environment={environment} metadataError={!!error} />
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
      setNotice(
        "Current names and versions refreshed. Values cannot be read back; review metadata before entering a new change.",
      );
    } catch (error) {
      if (!(await expireSession(error)))
        setFailure("Metadata refresh failed. Refresh before saving again.");
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
        setFailure("The secret form could not be read. Re-enter the change.");
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
      setFailure(`${problem} Values were cleared; enter them again.`);
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
      setNotice(
        `Secret version ${result.environment.latestSecretVersionId} saved. Running applications were not restarted; queued releases keep their captured version.`,
      );
      setNeedsRefresh(true);
      try {
        await queryClient.invalidateQueries({
          queryKey: environmentListKey(environment.projectId),
        });
        await reload();
      } catch (refreshError) {
        if (!(await expireSession(refreshError)))
          setFailure(
            "Saved, but metadata refresh failed. Refresh before entering another change.",
          );
      }
    } catch (error) {
      if (await expireSession(error)) return;
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        setNeedsRefresh(true);
        setFailure(
          "Settings or secrets changed elsewhere. Current metadata is being refreshed. Review it and re-enter values before saving again.",
        );
        try {
          await reload();
        } catch (refreshError) {
          if (!(await expireSession(refreshError)))
            setFailure(
              "The environment changed and refresh failed. Refresh before saving again.",
            );
        }
      } else if (mutationRejected(error)) {
        setFailure(
          "The secret change was refused. Check names, sizes and removals, then re-enter values. No values were retained for retry.",
        );
      } else {
        setNeedsRefresh(true);
        setFailure(
          "Save outcome unknown. No values were retained for retry. Refresh names and versions, review them, then re-enter any required change.",
        );
      }
    } finally {
      setPending(false);
    }
  }

  return (
    <>
      <p className="notice">
        Only explicit changes are saved. Omitted names retain their values; an
        empty value is valid. Secrets apply on the next eligible deployment and
        do not change running applications or already queued release snapshots.
      </p>
      {stale && (
        <p role="alert">
          Settings or secrets changed since this form opened. Refresh metadata
          before saving.
        </p>
      )}
      {metadataError && (
        <p role="alert">Metadata refresh failed. Refresh before saving.</p>
      )}
      <form
        ref={form}
        className="settings-form"
        onSubmit={(event) => void submit(event)}
        autoComplete="off"
        aria-busy={pending}
      >
        <fieldset className="form-fields" disabled={blocked}>
          <legend>Set or replace secret values</legend>
          {rows.map((row, index) => (
            <fieldset className="domain-row" key={row}>
              <legend>Secret change {index + 1}</legend>
              <label htmlFor={`name-${row}`}>Name</label>
              <input
                id={`name-${row}`}
                name={`name-${row}`}
                required
                maxLength={128}
                pattern="[A-Za-z_][A-Za-z0-9_]*"
                autoComplete="off"
                spellCheck={false}
              />
              <label htmlFor={`value-${row}`}>
                Value (visible; empty is valid)
              </label>
              <textarea
                id={`value-${row}`}
                name={`value-${row}`}
                data-secret-value=""
                rows={4}
                wrap="soft"
                maxLength={16384}
                autoComplete="off"
                spellCheck={false}
                aria-describedby="secret-value-help"
              />
              <Button
                type="button"
                variant="secondary"
                onClick={() => setRows(rows.filter((entry) => entry !== row))}
              >
                Discard change {index + 1}
              </Button>
            </fieldset>
          ))}
          <Button
            type="button"
            disabled={rows.length >= 64}
            onClick={() => setRows([...rows, crypto.randomUUID()])}
          >
            Add secret value ({rows.length}/64)
          </Button>
          <p id="secret-value-help" className="muted">
            Values are visible while editing and support multiple lines. They
            are cleared before a save request. Existing values are never filled
            in.
          </p>
          <fieldset>
            <legend>Remove saved secrets</legend>
            {base.secretNames.length ? (
              base.secretNames.map((name) => (
                <label className="checkbox-label" key={name}>
                  <input type="checkbox" name="remove" value={name} />
                  <span>Remove {name}</span>
                </label>
              ))
            ) : (
              <p className="muted">No saved names to remove.</p>
            )}
          </fieldset>
        </fieldset>
        {failure && (
          <p className="form-error" role="alert">
            {failure}
          </p>
        )}
        {notice && <p role="status">{notice}</p>}
        <div className="form-actions">
          <Button type="submit" variant="primary" disabled={blocked}>
            Save secret changes
          </Button>
          <Button
            type="button"
            variant="secondary"
            disabled={pending}
            onClick={() => void refresh()}
          >
            Discard changes and refresh metadata
          </Button>
        </div>
      </form>
    </>
  );
}
