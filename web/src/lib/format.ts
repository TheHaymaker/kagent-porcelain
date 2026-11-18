import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ActionKind, ApprovalState } from "@/gen/workbench/v1/approvals_pb";
import { AppStatus, EnvironmentName } from "@/gen/workbench/v1/apps_pb";
import type { Person } from "@/gen/workbench/v1/common_pb";

export function money(cents: number | bigint | undefined): string {
  const value = Number(cents ?? 0) / 100;
  return value.toLocaleString(undefined, { style: "currency", currency: "USD" });
}

export function toDate(ts: Timestamp | undefined): Date | undefined {
  return ts ? timestampDate(ts) : undefined;
}

export function relativeTime(input: Timestamp | Date | string | undefined, now = Date.now()): string {
  if (!input) return "";
  const date = input instanceof Date ? input : typeof input === "string" ? new Date(input) : timestampDate(input);
  const diff = (date.getTime() - now) / 1000;
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (abs < 45) return "just now";
  if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), "hour");
  if (abs < 86400 * 7) return rtf.format(Math.round(diff / 86400), "day");
  return date.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

export function fullTime(input: Timestamp | Date | undefined): string {
  if (!input) return "";
  const date = input instanceof Date ? input : timestampDate(input);
  return date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function personName(person: Person | undefined): string {
  if (!person) return "Someone";
  return person.displayName || person.email || person.id || "Someone";
}

export const KIND_LABEL: Record<ActionKind, string> = {
  [ActionKind.UNSPECIFIED]: "Approval",
  [ActionKind.ORDINARY]: "Routine step",
  [ActionKind.PRODUCTION_DEPLOY]: "Production deploy",
  [ActionKind.IAM_CHANGE]: "IAM change",
  [ActionKind.DATA_DELETION]: "Data deletion",
  [ActionKind.NEW_SPEND]: "New spend",
};

export const KIND_TONE: Record<ActionKind, Tone> = {
  [ActionKind.UNSPECIFIED]: "neutral",
  [ActionKind.ORDINARY]: "neutral",
  [ActionKind.PRODUCTION_DEPLOY]: "warning",
  [ActionKind.IAM_CHANGE]: "danger",
  [ActionKind.DATA_DELETION]: "danger",
  [ActionKind.NEW_SPEND]: "warning",
};

export const APPROVAL_STATE_LABEL: Record<ApprovalState, string> = {
  [ApprovalState.UNSPECIFIED]: "Unknown",
  [ApprovalState.PENDING]: "Waiting",
  [ApprovalState.APPROVED]: "Approved",
  [ApprovalState.REJECTED]: "Refused",
  [ApprovalState.SUPERSEDED]: "No longer needed",
};

export const APPROVAL_STATE_TONE: Record<ApprovalState, Tone> = {
  [ApprovalState.UNSPECIFIED]: "neutral",
  [ApprovalState.PENDING]: "info",
  [ApprovalState.APPROVED]: "success",
  [ApprovalState.REJECTED]: "danger",
  [ApprovalState.SUPERSEDED]: "neutral",
};

export const APP_STATUS_LABEL: Record<AppStatus, string> = {
  [AppStatus.UNSPECIFIED]: "Unknown",
  [AppStatus.BUILDING]: "Building",
  [AppStatus.PREVIEW]: "In preview",
  [AppStatus.LIVE]: "Live",
  [AppStatus.FAILED]: "Needs attention",
  [AppStatus.ARCHIVED]: "Archived",
};

export const APP_STATUS_TONE: Record<AppStatus, Tone> = {
  [AppStatus.UNSPECIFIED]: "neutral",
  [AppStatus.BUILDING]: "info",
  [AppStatus.PREVIEW]: "info",
  [AppStatus.LIVE]: "success",
  [AppStatus.FAILED]: "danger",
  [AppStatus.ARCHIVED]: "neutral",
};

export const ENV_LABEL: Record<EnvironmentName, string> = {
  [EnvironmentName.UNSPECIFIED]: "Environment",
  [EnvironmentName.PREVIEW]: "Preview",
  [EnvironmentName.PRODUCTION]: "Production",
};

export type Tone = "neutral" | "info" | "success" | "warning" | "danger";

/** Only https links are ever put in an href. */
export function safeHttpsUrl(url: unknown): string | undefined {
  if (typeof url !== "string") return undefined;
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" ? parsed.toString() : undefined;
  } catch {
    return undefined;
  }
}

export function newRequestId(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `req-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}
