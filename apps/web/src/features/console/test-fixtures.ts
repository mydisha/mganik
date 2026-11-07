// Data contoh untuk test Command Center (bukan data produksi).
import type { AnomalyItem, ConflictItem, Device, RunoutItem, StockItem, Summary } from "./api";

export const NOW = "2026-11-07T03:00:00Z";

export function summaryFixture(overrides: Partial<Summary> = {}): Summary {
  return {
    generated_at: NOW,
    runout: {
      window_days: 28,
      critical_items: 0,
      critical_locations: 0,
      warning_items: 0,
      warning_locations: 0,
      insufficient_data_items: 0,
      earliest_runout_date: null,
    },
    stock: { out_of_stock_items: 0, out_of_stock_locations: 0 },
    anomalies: { open_total: 0, open_by_severity: { LOW: 0, MEDIUM: 0, HIGH: 0, CRITICAL: 0 }, top_party: null },
    conflicts: { open_total: 0 },
    stores: { total: 3, offline: 0 },
    ...overrides,
  };
}

export function runoutFixture(overrides: Partial<RunoutItem> = {}): RunoutItem {
  return {
    location_id: "0193a000-0000-7000-8000-00000000a001",
    location_code: "ST-JKS",
    location_name: "Toko Jakarta Selatan",
    location_type: "OWN_STORE",
    product_id: "0193a000-0000-7000-8000-00000000b001",
    sku: "MG-BRS-5KG",
    product_name: "Beras Organik 5 kg",
    available_qty: 3,
    is_estimate: false,
    sold_qty: 56,
    history_days: 28,
    avg_daily_sales: 2,
    days_of_inventory: 1.5,
    runout_date: "2026-11-08",
    level: "CRITICAL",
    reasons: ["BELOW_LEAD_TIME"],
    has_policy: true,
    min_qty: 5,
    lead_time_days: 3,
    safety_days: 2,
    ...overrides,
  };
}

export function stockFixture(overrides: Partial<StockItem> = {}): StockItem {
  return {
    location_id: "0193a000-0000-7000-8000-00000000a001",
    location_code: "ST-JKS",
    location_name: "Toko Jakarta Selatan",
    location_type: "OWN_STORE",
    sync_state: "ONLINE",
    last_heartbeat_at: NOW,
    product_id: "0193a000-0000-7000-8000-00000000b001",
    sku: "MG-BRS-5KG",
    product_name: "Beras Organik 5 kg",
    physical_qty: 12,
    reserved_qty: 2,
    quarantine_qty: 0,
    available_qty: 10,
    is_estimate: false,
    updated_at: NOW,
    ...overrides,
  };
}

export function anomalyFixture(overrides: Partial<AnomalyItem> = {}): AnomalyItem {
  return {
    id: "0193a000-0000-7000-8000-00000000c001",
    rule_code: "OUT_OF_TERRITORY",
    rule_name: "Di luar wilayah distributor",
    severity: "HIGH",
    status: "OPEN",
    subject_type: "PARTY",
    subject_id: null,
    party_id: "0193a000-0000-7000-8000-00000000d001",
    party_name: "Distributor Sulsel",
    region_code: "31.74",
    region_name: "Jakarta Selatan",
    hit_count: 4,
    evidence: { serial_code: "7K3M9QX2TB4H8N6P", geo_source: "GPS" },
    first_seen_at: NOW,
    last_seen_at: NOW,
    resolved_by: null,
    resolved_at: null,
    resolution_note: null,
    version: 1,
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  };
}

export function conflictFixture(overrides: Partial<ConflictItem> = {}): ConflictItem {
  return {
    id: "0193a000-0000-7000-8000-00000000e001",
    kind: "OFFLINE_SALE_PREEMPT",
    status: "OPEN",
    location_id: "0193a000-0000-7000-8000-00000000a001",
    location_code: "ST-JKS",
    location_name: "Toko Jakarta Selatan",
    product_id: "0193a000-0000-7000-8000-00000000b001",
    sku: "MG-BRS-5KG",
    product_name: "Beras Organik 5 kg",
    qty: 1,
    pos_transaction_id: "0193a000-0000-7000-8000-00000000f001",
    reservation_id: null,
    order_id: "0193a000-0000-7000-8000-00000000f002",
    serial_unit_id: null,
    resolution: null,
    details: { reason: "preempt" },
    resolved_by: null,
    resolved_at: null,
    created_at: NOW,
    ...overrides,
  };
}

export function deviceFixture(overrides: Partial<Device> = {}): Device {
  return {
    id: "0193a000-0000-7000-8000-0000000d0001",
    location_id: "0193a000-0000-7000-8000-00000000a001",
    location_code: "ST-JKS",
    location_name: "Toko Jakarta Selatan",
    name: "Kasir depan",
    status: "ACTIVE",
    last_seen_at: null,
    last_acked_seq: 0,
    provisioned_by: null,
    revoked_at: null,
    created_at: NOW,
    ...overrides,
  };
}

export const emptyPage = { items: [], next_cursor: null };
export const emptyRunout = { items: [], next_cursor: null, window_days: 28, generated_at: NOW };
