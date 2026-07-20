export function setAliasTarget(source: Record<string, string>, alias: string, target: string) {
  return Object.fromEntries([...Object.entries(source).filter(([name]) => name !== alias.trim()), [alias.trim(), target]]
    .sort(([a], [b]) => a.localeCompare(b)));
}

export function removeAlias(source: Record<string, string>, alias: string) {
  return Object.fromEntries(Object.entries(source).filter(([name]) => name !== alias)
    .sort(([a], [b]) => a.localeCompare(b)));
}

export function validateAliasDraft(alias: string, target: string, modelNames: string[], aliases: Record<string, string>) {
  const name = alias.trim();
  if (!name || !target) return "Alias name and target are required.";
  if (modelNames.includes(name)) return "Alias collides with a concrete model.";
  if (!modelNames.includes(target)) return "Alias target is not defined.";
  if (Object.hasOwn(aliases, name)) return "Alias already exists.";
  return "";
}
