import type { Tab } from "./navigation";

export type RouteDataKey = "events" | "requests" | "billing" | "config";

export function routeDataKeysForTab(tab: Tab): RouteDataKey[] {
  switch (tab) {
    case "events":
      return ["events"];
    case "requests":
      return ["requests"];
    case "billing":
      return ["billing", "config"];
    case "configOps":
    case "advanced":
      return ["config"];
    case "dashboard":
    case "models":
    case "workers":
      return [];
  }
}

export function appendRoutePage<T>(current: T[], next: T[], offset: number): T[] {
  return offset === 0 ? next : [...current, ...next];
}
