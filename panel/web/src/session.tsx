import { Button } from "@cloudflare/kumo/components/button";
import { Code, ConnectError } from "@connectrpc/connect";
import { useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";

import { auth, errorMessage, queryClient, unauthenticated } from "./api";

export function Login() {
  const router = useRouter();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const form = event.currentTarget;
    const input = form.elements.namedItem("password");
    if (!(input instanceof HTMLInputElement)) return;
    const password = input.value;
    input.value = "";
    if (!password || new TextEncoder().encode(password).length > 1024) {
      setFailure("Enter a password of at most 1024 bytes.");
      input.focus();
      return;
    }
    setPending(true);
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
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="login">
      <section aria-labelledby="login-title">
        <p className="eyebrow">Private hosting</p>
        <h1 id="login-title" tabIndex={-1}>
          Sign in to your panel
        </h1>
        <p>Use the administrator password set during installation.</p>
        <form onSubmit={(event) => void submit(event)} aria-busy={pending}>
          <label htmlFor="password">Password</label>
          <input
            id="password"
            name="password"
            type="password"
            autoComplete="current-password"
            required
            maxLength={1024}
            readOnly={pending}
            aria-describedby={failure ? "login-error" : undefined}
            aria-invalid={!!failure}
          />
          {failure && (
            <p className="form-error" id="login-error" role="alert">
              {failure}
            </p>
          )}
          <Button type="submit" variant="primary" loading={pending}>
            Sign in
          </Button>
        </form>
        <p className="muted">
          Password resets require administrative access to the panel server.
        </p>
      </section>
    </main>
  );
}

export function Logout() {
  const router = useRouter();
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState("");

  async function logout() {
    if (pending) return;
    setPending(true);
    setFailure("");
    try {
      await queryClient.cancelQueries();
      try {
        await auth.logout({});
      } catch (error) {
        if (!unauthenticated(error)) throw error;
      }
      queryClient.clear();
      await router.navigate({ to: "/login", replace: true });
    } catch (error) {
      setFailure(errorMessage(error));
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="logout">
      <Button
        size="sm"
        variant="ghost"
        loading={pending}
        onClick={() => void logout()}
      >
        Sign out
      </Button>
      {failure && <p role="alert">{failure}</p>}
    </div>
  );
}
