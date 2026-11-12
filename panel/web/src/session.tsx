import { Button, Input, LayerCard, Text } from "@cloudflare/kumo";
import { Code, ConnectError } from "@connectrpc/connect";
import { CubeIcon, SignOutIcon } from "@phosphor-icons/react";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";

import { auth, errorMessage, queryClient, unauthenticated } from "./api";

export function Login() {
  const router = useRouter();
  const [failure, setFailure] = useState("");
  const login = useMutation({
    mutationFn: async (input: HTMLInputElement) => {
      const password = input.value;
      input.value = "";
      if (!password || new TextEncoder().encode(password).length > 1024) {
        setFailure("Enter a password of at most 1024 bytes.");
        input.focus();
        return;
      }
      setFailure("");
      try {
        // Passwords stay out of the query/mutation cache and browser storage.
        const result = await auth.login({ password });
        if (!result.authenticated)
          throw new ConnectError("Login failed", Code.Unauthenticated);
        await queryClient.cancelQueries();
        queryClient.clear();
        await router.navigate({
          to: "/projects",
          search: { cursor: "" },
          replace: true,
        });
      } catch (error) {
        setFailure(
          unauthenticated(error)
            ? "The password was not accepted."
            : errorMessage(error),
        );
        input.focus();
      }
    },
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (login.isPending) return;
    const input = event.currentTarget.elements.namedItem("password");
    if (input instanceof HTMLInputElement) login.mutate(input);
  }

  return (
    <main className="flex min-h-screen items-center justify-center p-4">
      <LayerCard className="w-full max-w-sm">
        <LayerCard.Primary className="flex flex-col gap-6 p-6">
          <div className="flex flex-col gap-2">
            <CubeIcon size={24} weight="bold" aria-hidden="true" />
            <Text variant="heading" size="lg" as="h1">
              <span id="login-title" tabIndex={-1}>
                Sign in to Panel
              </span>
            </Text>
            <Text variant="secondary">
              Use the administrator password you set during setup.
            </Text>
          </div>
          <form
            className="flex flex-col gap-4"
            onSubmit={submit}
            aria-busy={login.isPending}
            aria-labelledby="login-title"
          >
            {/* Uncontrolled so the password never enters React state. */}
            <Input
              label="Password"
              name="password"
              type="password"
              autoComplete="current-password"
              autoFocus
              required
              maxLength={1024}
              readOnly={login.isPending}
              error={failure || undefined}
            />
            <Button
              type="submit"
              variant="primary"
              loading={login.isPending}
              className="w-full"
            >
              Sign in
            </Button>
          </form>
          <Text variant="secondary" size="sm">
            Forgot it? Run <code>panel reset-password</code> on the server.
          </Text>
        </LayerCard.Primary>
      </LayerCard>
    </main>
  );
}

export function Logout() {
  const router = useRouter();
  const logout = useMutation({
    mutationFn: async () => {
      await queryClient.cancelQueries();
      try {
        await auth.logout({});
      } catch (error) {
        if (!unauthenticated(error)) throw error;
      }
      queryClient.clear();
      await router.navigate({ to: "/login", replace: true });
    },
  });

  return (
    <div className="flex flex-col gap-1">
      <Button
        size="sm"
        variant="ghost"
        loading={logout.isPending}
        onClick={() => logout.mutate()}
        icon={<SignOutIcon size={16} />}
      >
        Sign out
      </Button>
      {logout.error && (
        <Text variant="error" size="sm">
          <span role="alert">{errorMessage(logout.error)}</span>
        </Text>
      )}
    </div>
  );
}
