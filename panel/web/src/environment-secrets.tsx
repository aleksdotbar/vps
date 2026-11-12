import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { Fragment, useRef, useState } from "react";
import type { FormEvent } from "react";
import { KeyRoundIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertAction, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Item,
  ItemActions,
  ItemContent,
  ItemGroup,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item";

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
  ButtonLink,
  EnvironmentStatus,
  Page,
  PendingButton,
} from "./components";
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
        <Empty>
          <EmptyHeader>
            <EmptyTitle>Environment not found</EmptyTitle>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink to="/projects">Back to projects</ButtonLink>
          </EmptyContent>
        </Empty>
      </Page>
    );
  return (
    <Page
      title="Secrets"
      description="Environment variables your services read. Values are encrypted and never shown again."
      crumbs={[
        {
          label: environment.settings.name,
          link: {
            to: "/environments/$environmentId",
            params: { environmentId: id },
          },
        },
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
  // Value inputs stay uncontrolled so plaintext never enters React state;
  // bumping this remounts them, which drops what was typed.
  const [valueEpoch, setValueEpoch] = useState(0);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const fail = (title: string) => toast.error(title);
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
    try {
      await reload();
    } catch (error) {
      if (!(await expireSession(error)))
        fail("Couldn’t reload secrets. Try again.");
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
      toast.success("Saved. Deploy to apply the new values.");
      setNeedsRefresh(true);
      try {
        await queryClient.invalidateQueries({
          queryKey: environmentListKey(environment.projectId),
        });
        await reload();
      } catch (refreshError) {
        if (!(await expireSession(refreshError)))
          fail("Saved, but the page couldn’t reload. Refresh it.");
      }
    } catch (error) {
      if (await expireSession(error)) return;
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        setNeedsRefresh(true);
        fail(
          "Secrets were changed elsewhere. Showing the latest — enter your values again.",
        );
        try {
          await reload();
        } catch (refreshError) {
          if (!(await expireSession(refreshError)))
            fail("Secrets were changed elsewhere. Reload the page.");
        }
      } else if (mutationRejected(error)) {
        fail(
          "The change was refused. Check the names and sizes, then enter the values again.",
        );
      } else {
        setNeedsRefresh(true);
        fail(
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
        fail("The form couldn’t be read. Enter the values again.");
        return;
      }
      upserts.push({ name: name.value, value: value.value });
    }
    clearValues();
    const removeNames = [...removals];
    const problem = secretChangeProblem(upserts, removeNames, base.secretNames);
    if (problem) {
      fail(`${problem} For safety the values were cleared — enter them again.`);
      return;
    }
    saving.mutate({ upserts, removeNames });
  }

  return (
    <>
      {stale && (
        <Alert>
          <AlertTitle>Secrets changed since you opened this page.</AlertTitle>
          <AlertAction>
            <PendingButton
              size="sm"
              pending={refreshing.isPending}
              disabled={pending}
              onClick={() => refreshing.mutate()}
            >
              Load latest
            </PendingButton>
          </AlertAction>
        </Alert>
      )}
      {metadataError && (
        <Alert>
          <AlertTitle>Couldn’t refresh. Reload before saving.</AlertTitle>
        </Alert>
      )}
      <form
        ref={form}
        className="flex flex-col gap-6"
        onSubmit={submit}
        autoComplete="off"
        aria-busy={pending}
      >
        <fieldset disabled={blocked} className="flex flex-col gap-6">
          <Card>
            <CardHeader>
              <CardTitle>Saved secrets</CardTitle>
              {base.secretNames.length > 0 && (
                <CardDescription>
                  To change a value, add it again below with the same name.
                </CardDescription>
              )}
            </CardHeader>
            <CardContent>
              {base.secretNames.length ? (
                <ItemGroup>
                  {base.secretNames.map((name, index) => (
                    <Fragment key={name}>
                      {index > 0 && <ItemSeparator />}
                      <Item size="sm">
                        <ItemContent>
                          <ItemTitle className="font-mono">{name}</ItemTitle>
                        </ItemContent>
                        <ItemActions>
                          <Field orientation="horizontal">
                            <Checkbox
                              id={`remove-${name}`}
                              disabled={blocked}
                              checked={removals.includes(name)}
                              onCheckedChange={(checked) =>
                                setRemovals(
                                  checked
                                    ? [...removals, name]
                                    : removals.filter(
                                        (entry) => entry !== name,
                                      ),
                                )
                              }
                            />
                            <FieldLabel htmlFor={`remove-${name}`}>
                              <span className="sr-only">{name}: </span>Remove
                            </FieldLabel>
                          </Field>
                        </ItemActions>
                      </Item>
                    </Fragment>
                  ))}
                </ItemGroup>
              ) : (
                <Empty>
                  <EmptyHeader>
                    <EmptyMedia variant="icon">
                      <KeyRoundIcon />
                    </EmptyMedia>
                    <EmptyTitle>No secrets yet</EmptyTitle>
                    <EmptyDescription>
                      Add the values your Compose file expects below.
                    </EmptyDescription>
                  </EmptyHeader>
                </Empty>
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>Add or replace</CardTitle>
              <CardDescription>
                Values are masked and cleared as soon as you save. An empty
                value is allowed.
              </CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-4">
              {rows.map((row, index) => (
                <div
                  className="flex flex-wrap items-end gap-2"
                  key={row}
                  role="group"
                  aria-label={`Secret ${index + 1}`}
                >
                  <Field className="min-w-40 flex-1">
                    <FieldLabel htmlFor={`name-${row}`}>Name</FieldLabel>
                    <Input
                      id={`name-${row}`}
                      name={`name-${row}`}
                      className="font-mono"
                      required
                      maxLength={128}
                      pattern="[A-Za-z_][A-Za-z0-9_]*"
                      autoComplete="off"
                      spellCheck={false}
                      placeholder="API_TOKEN"
                    />
                  </Field>
                  <Field className="min-w-48 flex-2">
                    <FieldLabel htmlFor={`value-${row}`}>Value</FieldLabel>
                    <Input
                      key={`${row}:${valueEpoch}`}
                      id={`value-${row}`}
                      name={`value-${row}`}
                      type="password"
                      autoComplete="new-password"
                      maxLength={16384}
                      spellCheck={false}
                    />
                  </Field>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={`Discard secret ${index + 1}`}
                    onClick={() =>
                      setRows(rows.filter((entry) => entry !== row))
                    }
                  >
                    <Trash2Icon />
                  </Button>
                </div>
              ))}
              <div>
                <Button
                  type="button"
                  variant="outline"
                  disabled={rows.length >= 64}
                  onClick={() => setRows([...rows, crypto.randomUUID()])}
                >
                  <PlusIcon data-icon="inline-start" />
                  Add secret
                </Button>
              </div>
            </CardContent>
          </Card>
        </fieldset>
        <div className="flex flex-wrap items-center gap-2">
          <PendingButton
            type="submit"
            pending={saving.isPending}
            disabled={blocked}
          >
            Save secrets
          </PendingButton>
          <PendingButton
            type="button"
            variant="ghost"
            pending={refreshing.isPending}
            disabled={pending}
            onClick={() => refreshing.mutate()}
          >
            Discard changes
          </PendingButton>
        </div>
      </form>
    </>
  );
}
