package communication

import "time"

type storageMaintenanceStatus struct {
	LastAttempt time.Time
	LastSuccess time.Time
	LastError   string
}

func (g *Gateway) maintainStorage(now time.Time) {
	snapshot := g.spool.storage.Snapshot()
	g.maintenanceMu.Lock()
	interval := time.Hour
	if g.maintenance.LastError != "" || snapshot.LogicalBytes >= g.spool.limits.MaxBytes-g.spool.limits.MaxBytes/5 || snapshot.Files >= g.spool.limits.MaxOwnedFiles-g.spool.limits.MaxOwnedFiles/5 {
		interval = 5 * time.Minute
	}
	if !g.maintenance.LastAttempt.IsZero() && now.Sub(g.maintenance.LastAttempt) < interval {
		g.maintenanceMu.Unlock()
		return
	}
	g.maintenance.LastAttempt = now
	g.maintenanceMu.Unlock()
	retention := g.config.SpoolRetention
	if retention <= 0 {
		retention = 72 * time.Hour
	}
	err := g.spool.Compact(now, retention, CompactLimits{MaxTerminalFiles: g.config.SpoolMaxFiles, ArchiveMaxBytes: g.config.SpoolMaxArchiveBytes, MaxUsageEntries: g.config.SpoolMaxUsageEntries})
	g.maintenanceMu.Lock()
	defer g.maintenanceMu.Unlock()
	if err != nil {
		g.maintenance.LastError = "cleanup_failed"
		return
	}
	g.maintenance.LastSuccess = now
	g.maintenance.LastError = ""
}
