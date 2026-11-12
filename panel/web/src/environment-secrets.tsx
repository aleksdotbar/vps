import {
  Banner,
  Button,
  Checkbox,
  Empty,
  Input,
  LayerCard,
  SensitiveInput,
  Text,
} from "@cloudflare/kumo";
import { KeyIcon, PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
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
import { ButtonLink, EnvironmentStatus, Page } from "./components";
import { secretChangeProblem } from "./secret-changes";
import { useSessionExpiry } from "./use-session-expiry";

const route = getRouteApi("/_private/environments/$environmentId/secrets");

type Upsert = Pick<SecretValue, "name" | "value">;

export function EnvironmentSecretsRoute() {
  return <EnvironmentSecrets id={route.useParams().environmentId} />;
}

export function EnvironmentSecrets({ id }: { id: string }) {
  const { data, error } = useSuspenseQuery(environmentQuery(id));
  const environment = data.environment;
  if (!environment?.settings)
    return (
      <Page title="Environment not found">
        <Empty
          title="Environment not found"
          contents={
            <ButtonLink to="/projects" search={{ cursor: "" }}>
              Back to projects
            </ButtonLink>
          }
        />
      </Page>
    );
  return (
    <Page
      title="Secrets"
      description="Environment variables your services read. Values are encrypted and never shown again."
      crumbs={[
        { label: environment.settings.name, href: `/environments/${id}` },
      ]}
      actions={<EnvironmentStatus environment={environment} />}
    >
      <div className="flex max-w-3xl flex-col gap-6">
        <SecretForm
          key={id}
          environment={environment}
          metadataError={!!error}
        />
      </div>
    </Page>
  );
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
  const [removals, setRemovals] = useState<string[]>([]);
  // Bumping this remounts every value input, which drops its typed value.
  const [valueEpoch, setValueEpoch] = useState(0);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const refreshing = useMutation({ mutationFn: refresh });
  const saving = useMutation({ mutationFn: save });
  const pending = refreshing.isPending || saving.isPending;
  const stale = environment.version !== base.version;
  const blocked = pending || needsRefresh || stale || metadataError;

  function clearValues() {
    setValueEpoch((epoch) => epoch + 1);
  }

  async function reload() {
    clearValues();
    const current = await refreshEnvironment(environment.id);
    form.current?.reset();
    setBase(current);
    setRows([]);
    setRemovals([]);
    setNeedsRefresh(false);
  }

  async function refresh() {
    setFailure("");
    setNotice("");
    try {
      await reload();
      setNotice("Unsaved changes discarded.");
    } catch (error) {
      if (!(await expireSession(error)))
        setFailure("Couldn’t reload secrets. Try again.");
    }
  }

  async function save({
    upserts,
    removeNames,
  }: {
    upserts: Upsert[];
    removeNames: string[];
  }) {
    try {
      // Plaintext leaves only in this request; the values are blanked right after.
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
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    const submittedForm = event.currentTarget;
    const upserts: Upsert[] = [];
    for (const row of rows) {
      const name = submittedForm.elements.namedItem(`name-${row}`);
      const value = submittedForm.elements.namedItem(`value-${row}`);
      if (
        !(name instanceof HTMLInputElement) ||
        !(value instanceof HTMLInputElement)
      ) {
        clearValues();
        setFailure("The form couldn’t be read. Enter the values again.");
        return;
      }
      upserts.push({ name: name.value, value: value.value });
    }
    clearValues();
    const removeNames = [...removals];
    setFailure("");
    setNotice("");
    const problem = secretChangeProblem(upserts, removeNames, base.secretNames);
    if (problem) {
      setFailure(
        `${problem} For safety the values were cleared — enter them again.`,
      );
      return;
    }
    saving.mutate({ upserts, removeNames });
  }

  return (
    <>
      {stale && (
        <Banner
          variant="alert"
          title="Secrets changed since you opened this page."
          action={
            <Button
              size="sm"
              loading={refreshing.isPending}
              disabled={pending}
              onClick={() => refreshing.mutate()}
            >
              Load latest
            </Button>
          }
        />
      )}
      {metadataError && (
        <Banner
          variant="alert"
          title="Couldn’t refresh. Reload before saving."
        />
      )}
      <form
        ref={form}
        className="flex flex-col gap-6"
        onSubmit={submit}
        autoComplete="off"
        aria-busy={pending}
      >
        <fieldset disabled={blocked} className="flex flex-col gap-6">
          <LayerCard>
            <LayerCard.Secondary>Saved secrets</LayerCard.Secondary>
            <LayerCard.Primary className="flex flex-col gap-3">
              {base.secretNames.length ? (
                <>
                  <Text variant="secondary">
                    To change a value, add it again below with the same name.
                  </Text>
                  <ul className="flex flex-col divide-y divide-kumo-line">
                    {base.secretNames.map((name) => (
                      <li
                        key={name}
                        className="flex items-center justify-between gap-4 py-2"
                      >
                        <Text variant="mono">{name}</Text>
                        <Checkbox
                          label="Remove"
                          aria-label={`Remove ${name}`}
                          disabled={blocked}
                          checked={removals.includes(name)}
                          onCheckedChange={(checked) =>
                            setRemovals(
                              checked
                                ? [...removals, name]
                                : removals.filter((entry) => entry !== name),
                            )
                          }
                        />
                      </li>
                    ))}
                  </ul>
                </>
              ) : (
                <Empty
                  size="sm"
                  icon={<KeyIcon size={32} />}
                  title="No secrets yet"
                  description="Add the values your Compose file expects below."
                />
              )}
            </LayerCard.Primary>
          </LayerCard>
          <LayerCard>
            <LayerCard.Secondary>Add or replace</LayerCard.Secondary>
            <LayerCard.Primary className="flex flex-col gap-4">
              <Text variant="secondary">
                Values are masked and cleared as soon as you save. An empty
                value is allowed.
              </Text>
              {rows.map((row, index) => (
                <div
                  className="flex flex-wrap items-end gap-2"
                  key={row}
                  role="group"
                  aria-label={`Secret ${index + 1}`}
                >
                  <Input
                    id={`name-${row}`}
                    name={`name-${row}`}
                    label="Name"
                    className="min-w-40 flex-1 font-mono"
                    required
                    maxLength={128}
                    pattern="[A-Za-z_][A-Za-z0-9_]*"
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="API_TOKEN"
                  />
                  <SensitiveInput
                    key={`${row}:${valueEpoch}`}
                    id={`value-${row}`}
                    name={`value-${row}`}
                    label="Value"
                    className="min-w-48 flex-2"
                    disabled={blocked}
                    maxLength={16384}
                    spellCheck={false}
                  />
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
            </LayerCard.Primary>
          </LayerCard>
        </fieldset>
        {failure && (
          <Text variant="error" role="alert">
            {failure}
          </Text>
        )}
        {notice && (
          <Text variant="secondary" role="status">
            {notice}
          </Text>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="submit"
            variant="primary"
            loading={saving.isPending}
            disabled={blocked}
          >
            Save secrets
          </Button>
          <Button
            type="button"
            variant="ghost"
            loading={refreshing.isPending}
            disabled={pending}
            onClick={() => refreshing.mutate()}
          >
            Discard changes
          </Button>
        </div>
      </form>
    </>
  );
}
