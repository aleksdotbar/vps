export function githubDestination(
  value: string,
  phase: "manifest" | "installation",
) {
  if (value.length > 2048) throw new Error("Invalid GitHub destination");
  const destination = new URL(value);
  const path =
    phase === "manifest"
      ? /^(?:\/settings\/apps\/new|\/organizations\/[A-Za-z0-9][A-Za-z0-9-]{0,99}\/settings\/apps\/new)$/
      : /^\/apps\/[A-Za-z0-9][A-Za-z0-9-]{0,99}\/installations\/new$/;
  if (
    destination.origin !== "https://github.com" ||
    destination.username ||
    destination.password ||
    destination.hash ||
    !path.test(destination.pathname) ||
    [...destination.searchParams.keys()].length !== 1 ||
    !/^[0-9a-f]{64}$/.test(destination.searchParams.get("state") ?? "")
  )
    throw new Error("Invalid GitHub destination");
  return destination.href;
}

export function githubRegistration(
  actionUrl: string,
  manifestJson: string,
  origin: string,
) {
  const action = githubDestination(actionUrl, "manifest");
  const panelOrigin = new URL(origin);
  if (panelOrigin.protocol !== "https:" || panelOrigin.origin !== origin)
    throw new Error("Invalid panel origin");
  if (manifestJson.length > 16_384) throw new Error("Invalid GitHub manifest");
  const manifest: unknown = JSON.parse(manifestJson);
  if (
    !manifest ||
    typeof manifest !== "object" ||
    !("url" in manifest) ||
    manifest.url !== origin ||
    !("redirect_url" in manifest) ||
    manifest.redirect_url !== `${origin}/github/manifest` ||
    !("setup_url" in manifest) ||
    manifest.setup_url !== `${origin}/github/installation`
  )
    throw new Error("Invalid GitHub manifest");
  return { action, manifestJson };
}
