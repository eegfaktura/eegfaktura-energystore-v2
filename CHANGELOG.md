# Changelog

All notable changes to **eegfaktura-energystore-v2** (Go energy data store, TimescaleDB) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and
versioning follows the deployment release tags. Detailed diffs stay in the
`git log`; this changelog highlights the changes relevant for overview and
operations.

## [Unreleased]

### Added
- **`POST /eeg/v2/{ecid}/billing-check`**, same request and response as energystore v1 (platform#148):
  L3 quarter hours with days and first/last quarter hour with a value per requested entry, totals of all
  metering points of the community. Evaluated on the raw rows of `energy_data`, not on the aggregates
  (their `MIN(qov)` hides L3); a cell without a row counts as L0, as in v1. An L3 in one OBIS variant
  (e.g. G.01 vs. G.01T) counts even if the other variant is L1. Known difference to v1: on the autumn
  clock change v2 keeps both 02:00–02:45 hours. No schema change.

### Security
- `github.com/xuri/excelize/v2` 2.10.1 -> 2.11.0, closing CVE-2026-54063 (CVSS 7.5).
  The library backs both the XLSX import and the Excel export, so it processes files
  supplied from outside. Full test suite green, including `internal/xlsximport` and
  `internal/excelexport`. The `go` directive is unchanged.

### Fixed
- Raw-data query returned each timestamp multiple times for a re-registered
  metering point (same root cause as v1). When a metering point was
  deregistered from one member and re-registered under another, the old
  participant row remained with an overlapping active window, so the caller's
  `cps` list contained that metering point more than once for queries
  overlapping that window. The store holds exactly one data series per
  metering-point name, so each duplicate `cps` entry produced an identical
  repeated series ("each timestamp 4×"; support cases RC101586, RC105720).
  `QueryRawData` now de-duplicates the target list by metering-point name
  before querying — covering both raw endpoints (`/query/rawdata` and
  `/eeg/v2/{ecid}/raw`) and preventing the `Aggregate` function from
  double-counting.
