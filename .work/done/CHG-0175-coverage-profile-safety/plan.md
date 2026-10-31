# CHG-0175: Reject malformed coverage locations without panic

Chain: engineering quality gate → hosting CHG-0174 → this plan → state.yaml.
Observed predecessor coverage aggregation panic at devcheck.go:60: a truncated
location row lacked a colon. This is independent of hosting behavior, but blocks
honest quality evidence. Do not skip malformed rows or weaken coverage floors.

One slice: add a regression for a three-column row without a location separator;
reject it before slicing, preserving existing short synthetic location contracts.
Use LastIndexByte so a Windows drive prefix is not mistaken for the separator.
Use a unique temporary profile per package to avoid cross-package stale writers;
validate each profile before merging and name the failing package. Keep all
original tests/statements in the aggregate, no ignore/exclusion rules.

Acceptance: TestCoverageRejectsMissingLocation and all devcheck tests pass;
just go-check remains authoritative and any upstream malformed profile or real
coverage failure is reported. No Docker operations or dependency changes.
