import { Code, ConnectError } from "@connectrpc/connect";
import { useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";

import { CubeIcon, SignOutIcon } from "@phosphor-icons/react";

import { auth, errorMessage, queryClient, unauthenticated } from "./api";
import { Button, Card } from "./components";

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
      <div className="login-card">
        <Card>
          <div className="stack-sm">
            <span className="brand" style={{ padding: 0 }}>
              <span className="brand-mark" aria-hidden="true">
                <CubeIcon size={16} weight="bold" />
              </span>
            </span>
            <h1 id="login-title" tabIndex={-1}>
              Sign in to Panel
            </h1>
            <p className="muted">
              Use the administrator password you set during setup.
            </p>
          </div>
          <form
            className="form"
            onSubmit={(event) => void submit(event)}
            aria-busy={pending}
            aria-labelledby="login-title"
          >
            <div className="field">
              <label htmlFor="password">Password</label>
              <input
                id="password"
                name="password"
                type="password"
                autoComplete="current-password"
                autoFocus
                required
                maxLength={1024}
                readOnly={pending}
                aria-describedby={failure ? "login-error" : undefined}
                aria-invalid={!!failure}
              />
              {failure && (
                <p className="field-error" id="login-error" role="alert">
                  {failure}
                </p>
              )}
            </div>
            <Button
              type="submit"
              variant="primary"
              pending={pending}
              style={{ width: "100%" }}
            >
              Sign in
            </Button>
          </form>
          <p className="help">
            Forgot it? Run <code>panel reset-password</code> on the server.
          </p>
        </Card>
      </div>
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
    <div className="stack-sm">
      <Button
        size="sm"
        variant="ghost"
        pending={pending}
        onClick={() => void logout()}
        icon={<SignOutIcon size={16} />}
        aria-label="Sign out"
      >
        <span className="brand-text">Sign out</span>
      </Button>
      {failure && (
        <p className="field-error" role="alert">
          {failure}
        </p>
      )}
    </div>
  );
}
