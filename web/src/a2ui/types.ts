/** A2UI v0.9.1 wire types, as far as the Workbench catalogue needs them. */

export const WORKBENCH_CATALOG_ID = "https://workbench.dev/catalogs/workbench/v1/catalog.json";

export interface A2uiComponent {
  id: string;
  component: string;
  [prop: string]: unknown;
}

export interface CreateSurface {
  surfaceId: string;
  catalogId?: string;
  [extra: string]: unknown;
}
export interface UpdateComponents {
  surfaceId: string;
  components: A2uiComponent[];
}
export interface UpdateDataModel {
  surfaceId: string;
  path?: string;
  value?: unknown;
}
export interface DeleteSurface {
  surfaceId: string;
}

export type A2uiMessage =
  | { version?: string; createSurface: CreateSurface }
  | { version?: string; updateComponents: UpdateComponents }
  | { version?: string; updateDataModel: UpdateDataModel }
  | { version?: string; deleteSurface: DeleteSurface };

export interface SurfaceState {
  surfaceId: string;
  catalogId: string;
  components: Readonly<Record<string, A2uiComponent>>;
  dataModel: unknown;
  deleted: boolean;
  /** Bumped on every change, for cheap memoisation. */
  revision: number;
}

/** A property that is either a literal or a data binding `{path}`. */
export type Dynamic<T> = T | { path: string };
