import type { SecretValue } from "../../gen/ts/panel/v1/panel_pb";

export function secretChangeProblem(
  upserts: readonly Pick<SecretValue, "name" | "value">[],
  removals: readonly string[],
  existingNames: readonly string[],
): string | undefined {
  if (!upserts.length && !removals.length)
    return "Add a value or select a saved name to remove.";
  if (upserts.length > 64 || removals.length > 64)
    return "Change at most 64 values and remove at most 64 names at once.";
  const namePattern = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
  const added = new Set<string>();
  const removed = new Set<string>();
  const encoder = new TextEncoder();
  for (const item of upserts) {
    if (!namePattern.test(item.name))
      return "Use secret names of at most 128 ASCII letters, digits or underscores, starting with a letter or underscore.";
    if (added.has(item.name)) return "Each secret name may be set only once.";
    if (encoder.encode(item.value).length > 16_384)
      return "Each secret value must not exceed 16384 bytes.";
    added.add(item.name);
  }
  for (const name of removals) {
    if (!namePattern.test(name) || removed.has(name))
      return "Removal names must be valid and unique.";
    if (added.has(name))
      return "A secret cannot be set and removed in the same change.";
    removed.add(name);
  }
  const remaining = new Set(existingNames.filter((name) => !removed.has(name)));
  for (const name of added) remaining.add(name);
  if (remaining.size > 64)
    return "The environment may contain at most 64 secret names. Remove saved names before adding more.";
}
