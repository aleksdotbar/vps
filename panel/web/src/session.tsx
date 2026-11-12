import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { BoxIcon, LogOutIcon } from "lucide-react";
import { useState } from "react";
import type { FormEvent } from "react";
import { toast } from "sonner";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";

import { auth, errorMessage, queryClient, unauthenticated } from "./api";
import { PendingButton } from "./components";

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
    <main className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted p-6 md:p-10">
      <div className="flex w-full max-w-sm flex-col gap-6">
        <div className="flex items-center gap-2 self-center font-medium">
          <BoxIcon className="size-5" aria-hidden="true" />
          Panel
        </div>
        <Card>
          <CardHeader className="text-center">
            <CardTitle className="text-xl">
              <h1 id="login-title" tabIndex={-1}>
                Sign in to Panel
              </h1>
            </CardTitle>
            <CardDescription>
              Use the administrator password you set during setup.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form
              onSubmit={submit}
              aria-busy={login.isPending}
              aria-labelledby="login-title"
            >
              <FieldGroup>
                <Field data-invalid={!!failure || undefined}>
                  <FieldLabel htmlFor="password">Password</FieldLabel>
                  {/* Uncontrolled so the password never enters React state. */}
                  <Input
                    id="password"
                    name="password"
                    type="password"
                    autoComplete="current-password"
                    autoFocus
                    required
                    maxLength={1024}
                    readOnly={login.isPending}
                    aria-invalid={!!failure || undefined}
                  />
                  <FieldError>{failure}</FieldError>
                </Field>
                <Field>
                  <PendingButton
                    type="submit"
                    pending={login.isPending}
                    className="w-full"
                  >
                    Sign in
                  </PendingButton>
                  <FieldDescription className="text-center">
                    Forgot it? Run <code>panel reset-password</code> on the
                    server.
                  </FieldDescription>
                </Field>
              </FieldGroup>
            </form>
          </CardContent>
        </Card>
      </div>
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
    onError: (error) => toast.error(errorMessage(error)),
  });

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <SidebarMenuButton
          disabled={logout.isPending}
          onClick={() => logout.mutate()}
        >
          {logout.isPending ? <Spinner /> : <LogOutIcon />}
          <span>Sign out</span>
        </SidebarMenuButton>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
