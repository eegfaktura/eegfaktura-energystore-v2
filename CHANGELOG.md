# Changelog

All notable changes to **eegfaktura-energystore-v2** (Go energy data store, TimescaleDB) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and
versioning follows the deployment release tags. Detailed diffs stay in the
`git log`; this changelog highlights the changes relevant for overview and
operations.

## [Unreleased]

### Added
- Time-of-use window folding (ZVT), parity with v1: the participant report
  request (`POST /eeg/v2/{ecid}/report`) accepts up to two generic time
  windows per metering point (`timeWindows: [{key: T1|T2, from: "HH:MM",
  to: "HH:MM"}]`, 15-min raster, `from > to` crosses midnight). The report
  response then carries per-window sums of the billing quantity per metering
  point (`report.buckets: [{key: BASE|T1|T2, kWh}]`, consumer = utilization,
  producer = production − allocation). `BASE` is the residual against the
  period total, so the kWh partition is exact by construction. Membership is
  evaluated against the local wall-clock time of the row ids (container TZ
  Europe/Vienna). Invalid windows are rejected with 400.

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
