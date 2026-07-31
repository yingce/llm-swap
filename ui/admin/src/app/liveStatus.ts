import type { StatusResponse } from "../api";

export const STATUS_REFRESH_INTERVAL_MS = 5000;

export type LiveStatusState = {
  status: StatusResponse | null;
  error: string;
};

export type LiveStatusAction =
  | { type: "success"; status: StatusResponse }
  | { type: "failure"; error: string };

export function reduceLiveStatus(state: LiveStatusState, action: LiveStatusAction): LiveStatusState {
  if (action.type === "success") {
    return { status: action.status, error: "" };
  }
  return { status: state.status, error: action.error };
}
