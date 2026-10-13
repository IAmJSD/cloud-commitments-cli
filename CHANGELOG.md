# Changelog

All notable changes to the CUDly CLI are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Changed

- BREAKING: `--input-csv` now requires `Term` and `PaymentOption` columns in
  addition to `Service`, `Region`, `ResourceType` and `Count`. `Term` must be
  `1yr` or `3yr` and `PaymentOption` must be `all-upfront`, `partial-upfront`
  or `no-upfront` on every row. CSVs with only the older four columns, or with
  a blank or unknown value, now fail to load with an error naming the missing
  column or the row.
- Split the CLI out of the CUDly monorepo into its own module,
  `github.com/LeanerCloud/cloud-commitments-cli`. The shared client and
  provider code it depends on now lives in
  `github.com/LeanerCloud/cloud-commitments-go`, pinned in `go.mod`.
  History predating the split lives in the monorepo's changelog.
