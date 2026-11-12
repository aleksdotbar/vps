import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { FolderGit2Icon, RefreshCwIcon, TriangleAlertIcon } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { cn } from "@/lib/utils";

import { integrationQuery } from "./api";
import { CloudflareConnection } from "./cloudflare-connection";
import { ButtonLink, Detail, Details, Page, StatusBadge } from "./components";
import { GitHubSetup } from "./github-setup";

export function Settings() {
  const { data, error, refetch } = useSuspenseQuery(integrationQuery());
  const refresh = useMutation({ mutationFn: () => refetch() });
  return (
    <Page
      title="Settings"
      description="Connections this panel uses to build and publish your apps."
    >
      <div className="flex max-w-3xl flex-col gap-6">
        <Card id="github">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <FolderGit2Icon />
              GitHub
              {data.installed ? (
                <StatusBadge tone="success">Connected</StatusBadge>
              ) : data.registered ? (
                <StatusBadge tone="warning">Not installed</StatusBadge>
              ) : (
                <StatusBadge tone="neutral">Not connected</StatusBadge>
              )}
            </CardTitle>
            <CardDescription>
              Reads Compose files from the repositories you choose.
            </CardDescription>
            <CardAction>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Refresh GitHub status"
                disabled={refresh.isPending}
                onClick={() => refresh.mutate()}
              >
                <RefreshCwIcon
                  className={cn(refresh.isPending && "animate-spin")}
                />
              </Button>
            </CardAction>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {error && (
              <Alert>
                <TriangleAlertIcon />
                <AlertDescription>
                  Couldn’t refresh GitHub status. Details may be out of date.
                </AlertDescription>
              </Alert>
            )}
            {data.registered && (
              <Details>
                <Detail label="Owner">{data.owner || "—"}</Detail>
                <Detail label="App">
                  {data.installed ? "Installed" : "Created, not installed yet"}
                </Detail>
              </Details>
            )}
            <GitHubSetup
              registered={data.registered}
              installed={data.installed}
              actions={
                data.installed && (
                  <ButtonLink variant="outline" to="/settings/repositories">
                    Choose repositories
                  </ButtonLink>
                )
              }
            />
          </CardContent>
        </Card>
        <CloudflareConnection />
      </div>
    </Page>
  );
}
