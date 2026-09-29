# SPEC-0029: Hub build and disk boundary

Frozen: 2026-09-26.

1. The default hub runtime must not fetch or compile optional connector
   binaries. Slack MCP is built only as an explicitly selected ToolHub artifact.
2. Docker CLI and ToolHive belong only in the control image used by ToolHub and
   workload controller, not in the Hermes runtime, communication hub, Broker or
   CLIProxy image.
3. Core and control images share common BuildKit layers. A successful ordinary
   hub build/up reclaims superseded hub images pinned by stopped hub containers
   and limits the local builder cache to 8 GB without deleting named data volumes
   or unrelated containers.
4. Disk and build-time improvements are reported from measured host results;
   Docker logical reclaim is not counted as Windows free space until VHDX
   compaction has returned it to the host filesystem.
