import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { integrationQuery } from "./api";
import { Detail, Details, PageTitle } from "./components";
import { GitHubSetup } from "./github-setup";
import { CloudflareConnection } from "./cloudflare-connection";

export function Settings() {
  const { data, error } = useSuspenseQuery(integrationQuery());
  return (
    <>
      <PageTitle
        title="Settings"
        description="Connections owned by this installation."
      />
      {error && (
        <p role="alert">
          Refresh failed. Connection details may be out of date.
        </p>
      )}
      <section aria-labelledby="github-title">
        <h2 id="github-title">GitHub</h2>
        <Details>
          <Detail label="GitHub App">
            {data.registered ? "Registered" : "Not connected"}
          </Detail>
          <Detail label="Installation">
            {data.installed ? "Installed" : "Not installed"}
          </Detail>
          <Detail label="Owner">{data.owner || "Not configured"}</Detail>
        </Details>
        <GitHubSetup registered={data.registered} installed={data.installed} />
        {data.installed && (
          <p className="action-link">
            <Link to="/settings/repositories">
              Choose panel repository access
            </Link>
          </p>
        )}
      </section>
      <CloudflareConnection />
    </>
  );
}
