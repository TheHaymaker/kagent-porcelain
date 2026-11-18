// NOTICE: Adapted from kagent-dev/kagent ui/src/api/chat/a2aMetadata.ts, Apache-2.0.
export const A2A_METADATA = {
  outputSchemaSha256: "kagent.dev/a2a/output-schema-sha256",
  partType: "kagent.dev/a2a/part-type",
  timelinePosition: "kagent.dev/a2a/timeline-position",
  thought: "kagent.dev/a2a/thought",
} as const;

/** The media type marking an A2A DataPart whose data is an array of A2UI messages. */
export const A2UI_MIME_TYPE = "application/a2ui+json";

export function metadataString(
  metadata: Record<string, unknown> | undefined,
  key: string,
): string | undefined {
  const value = metadata?.[key];
  return typeof value === "string" ? value : undefined;
}
