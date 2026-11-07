import { api } from "@/shared/api/client";
import type { components, paths } from "@/shared/api/schema";
import { apiCall } from "@/shared/lib/api";

type Schemas = components["schemas"];
export type Summary = Schemas["CCSummaryResponse"];
export type StockItem = Schemas["CCStockItem"];
export type RunoutItem = Schemas["CCRunoutItem"];
export type RunoutPage = Schemas["CCRunoutResponse"];
export type RunoutLevel = Schemas["CCRunoutLevel"];
export type AnomalyItem = Schemas["CCAnomalyItem"];
export type AnomalyStatus = Schemas["CCAnomalyStatus"];
export type AnomalySeverity = Schemas["CCSeverity"];
export type AnomalyUpdate = Schemas["CCAnomalyUpdateRequest"];
export type ConflictItem = Schemas["CCConflictItem"];
export type ConflictResolve = Schemas["ConflictResolveRequest"];
export type LeakageItem = Schemas["CCLeakageItem"];
export type LeakagePage = Schemas["CCLeakageResponse"];
export type Trace = Schemas["TraceResponse"];
export type Device = Schemas["PosDevice"];
export type DeviceProvisioned = Schemas["PosDeviceProvisioned"];

type Query<P extends keyof paths> = paths[P] extends { get: { parameters: { query?: infer Q } } } ? NonNullable<Q> : never;
export type StockQuery = Query<"/api/v1/cc/stock">;
export type RunoutQuery = Query<"/api/v1/cc/runout">;
export type AnomalyQuery = Query<"/api/v1/cc/anomalies">;
export type ConflictQuery = Query<"/api/v1/cc/conflicts">;
export type LeakageQuery = Query<"/api/v1/cc/distribution-leakage">;
export type DeviceQuery = Query<"/api/v1/pos/devices">;

export interface CursorPage<T> {
  items: T[];
  next_cursor: string | null;
}

export const ccKeys = {
  all: ["cc"] as const,
  summary: () => ["cc", "summary"] as const,
  stock: (q: StockQuery) => ["cc", "stock", q] as const,
  runout: (q: RunoutQuery) => ["cc", "runout", q] as const,
  anomalies: (q: AnomalyQuery) => ["cc", "anomalies", q] as const,
  conflicts: (q: ConflictQuery) => ["cc", "conflicts", q] as const,
  leakage: (q: LeakageQuery) => ["cc", "leakage", q] as const,
  trace: (code: string) => ["serial", "trace", code] as const,
  devices: () => ["pos", "devices"] as const,
  stores: () => ["cc", "stores"] as const,
};

const json = { "Content-Type": "application/json" };

export async function getSummary(): Promise<Summary> {
  return (await apiCall(() => api.GET("/api/v1/cc/summary"))).data;
}

export async function listStock(query: StockQuery): Promise<CursorPage<StockItem>> {
  return (await apiCall(() => api.GET("/api/v1/cc/stock", { params: { query } }))).data;
}

export async function listRunout(query: RunoutQuery): Promise<RunoutPage> {
  return (await apiCall(() => api.GET("/api/v1/cc/runout", { params: { query } }))).data;
}

export async function listAnomalies(query: AnomalyQuery): Promise<CursorPage<AnomalyItem>> {
  return (await apiCall(() => api.GET("/api/v1/cc/anomalies", { params: { query } }))).data;
}

export async function updateAnomaly(id: string, body: AnomalyUpdate): Promise<AnomalyItem> {
  return (await apiCall(() => api.PATCH("/api/v1/cc/anomalies/{id}", { params: { path: { id } }, body }))).data;
}

export async function listConflicts(query: ConflictQuery): Promise<CursorPage<ConflictItem>> {
  return (await apiCall(() => api.GET("/api/v1/cc/conflicts", { params: { query } }))).data;
}

export async function resolveConflict(id: string, body: ConflictResolve): Promise<ConflictItem> {
  return (await apiCall(() => api.POST("/api/v1/cc/conflicts/{id}/resolve", { params: { path: { id } }, body }))).data;
}

export async function listLeakage(query: LeakageQuery): Promise<LeakagePage> {
  return (await apiCall(() => api.GET("/api/v1/cc/distribution-leakage", { params: { query } }))).data;
}

export async function traceSerial(code: string): Promise<Trace> {
  return (await apiCall(() => api.GET("/api/v1/serials/{code}/trace", { params: { path: { code } } }))).data;
}

export async function listDevices(query: DeviceQuery): Promise<CursorPage<Device>> {
  return (await apiCall(() => api.GET("/api/v1/pos/devices", { params: { query } }))).data;
}

export async function provisionDevice(body: Schemas["PosDeviceCreateRequest"]): Promise<DeviceProvisioned> {
  return (await apiCall(() => api.POST("/api/v1/pos/devices", { body }))).data;
}

export async function revokeDevice(id: string, reason: string): Promise<Device> {
  const body = reason ? { reason } : {};
  return (await apiCall(() => api.POST("/api/v1/pos/devices/{id}/revoke", { params: { path: { id } }, body, headers: json }))).data;
}
