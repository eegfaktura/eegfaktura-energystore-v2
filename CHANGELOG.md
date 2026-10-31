# Changelog

All notable changes to **eegfaktura-energystore-v2** (Go energy data store, TimescaleDB) are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and
versioning follows the deployment release tags. Detailed diffs stay in the
`git log`; this changelog highlights the changes relevant for overview and
operations.

## [Unreleased]

### Added
- **`POST /eeg/v2/{ecid}/billing-check`**, same request and response as energystore v1 (platform#148):
  L3 quarter hours with days, first/last quarter hour with a value and the missing quarter hours with
  their days (real quarter hours of the local day: 92/96/100) per requested entry, totals of all
  metering points of the community. Evaluated on the raw rows of `energy_data`, not on the aggregates
  (their `MIN(qov)` hides L3); a cell without a row counts as L0, as in v1. An L3 in one OBIS variant
  (e.g. G.01 vs. G.01T) counts even if the other variant is L1. Known difference to v1: on the autumn
  clock change v2 keeps both 02:00–02:45 hours. No schema change.

### Security
- **Bearer tokens: RS256 only** (platform#191, as backend#99). The go-oidc verifier of `NewKeycloakClient` took
  the allowed signing algorithms from the Keycloak discovery (HS* and `none` were already dropped); both variants
  (with `ESV2_AUTH_*_AUDIENCE`, and `SkipClientIDCheck` without) now accept RS256 only. A token signed with
  RS384/RS512/PS*/ES*/EdDSA under a realm key is rejected with 403 (`ProtectApp`, GraphQL; for `ProtectAPI` this is
  the id_token of the password grant, which the service fetches itself). Logins would not be affected: as of 2026-10-23
  the public JWKS of Prod/Pilot (login.eegfaktura.at) and Dev hold exactly one signing key (RS256), and the realm
  sets no different ID-token algorithm. If a realm ever got an active non-RS256 signing key (e.g. on key rotation),
  logins would fail with 403. Tests also cover HS256 signed with the realm's public key (PEM and DER) and
  `alg=none` (rejected) and, in the `SkipClientIDCheck` variant, a token whose `aud` is not the client ID.
- `github.com/xuri/excelize/v2` 2.11.0 -> `v2.11.1-0.20261003002531-6258dcebc4e2`, a temporary pin to the upstream
  commit of 2026-10-03 until v2.12.0 is released, closing the 15 open Dependabot alerts on excelize (#16–#30, 9 high
  and 6 medium; the same pin as energystore#103). Updated with it: `mscfb`, `efp` and `golang.org/x/{crypto,net,sync,sys,text,image}`.
  The `go` directive reads `1.26.0` instead of `1.26`, which is the same toolchain line (excelize requires ≥ 1.26.0).
  Excel export: the cell values are unchanged, because all floats were already rounded to 6 decimals. Malformed
  workbooks with a row number above Excel's limit are now cut off earlier on import, which drops the row before it.
- `github.com/xuri/excelize/v2` 2.10.1 -> 2.11.0, closing CVE-2026-54063 (CVSS 7.5).
  The library backs both the XLSX import and the Excel export, so it processes files
  supplied from outside. Full test suite green, including `internal/xlsximport` and
  `internal/excelexport`. The `go` directive is unchanged.

### Fixed
- **CI: the Snyk Code scan fails visibly when it cannot run.** An invalid `SNYK_TOKEN` ended the weekly and
  push scan green without any result (`continue-on-error`); now only „issues found“ keeps the run green (the
  issues go to Code Scanning), an authentication or tool error fails it. The Snyk CLI is pinned to 1.1308.1
  instead of the latest version.
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
