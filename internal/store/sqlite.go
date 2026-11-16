package store

import (
	"database/sql"
	"fmt"
	"time"

	"netwatch/internal/model"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	stmts := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA busy_timeout=5000;`,
		`CREATE TABLE IF NOT EXISTS counter_state (
			scope TEXT NOT NULL,
			key TEXT NOT NULL,
			last_rx INTEGER NOT NULL DEFAULT 0,
			last_tx INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (scope, key)
		);`,
		`CREATE TABLE IF NOT EXISTS daily_traffic (
			node_id TEXT NOT NULL,
			date TEXT NOT NULL,
			rx_bytes INTEGER NOT NULL DEFAULT 0,
			tx_bytes INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (node_id, date)
		);`,
		`CREATE TABLE IF NOT EXISTS resource_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			node_id TEXT NOT NULL,
			ts INTEGER NOT NULL,
			cpu_pct REAL NOT NULL,
			mem_pct REAL NOT NULL,
			disk_pct REAL NOT NULL,
			rx_bps REAL NOT NULL,
			tx_bps REAL NOT NULL,
			avg_latency_ms REAL NOT NULL,
			active_conns INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_samples_node_ts ON resource_samples(node_id, ts);`,
		`CREATE TABLE IF NOT EXISTS probe_results (
			node_id TEXT NOT NULL,
			name TEXT NOT NULL,
			tag TEXT NOT NULL,
			latency_ms REAL NOT NULL,
			loss_pct REAL NOT NULL,
			status TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (node_id, name)
		);`,
		`CREATE TABLE IF NOT EXISTS client_ip_traffic (
			node_id TEXT NOT NULL,
			ip TEXT NOT NULL,
			active_conns INTEGER NOT NULL DEFAULT 0,
			rx_bps REAL NOT NULL DEFAULT 0,
			tx_bps REAL NOT NULL DEFAULT 0,
			total_rx INTEGER NOT NULL DEFAULT 0,
			total_tx INTEGER NOT NULL DEFAULT 0,
			last_seen INTEGER NOT NULL,
			PRIMARY KEY (node_id, ip)
		);`,
		`CREATE TABLE IF NOT EXISTS ip_geo_cache (
			ip TEXT PRIMARY KEY,
			region TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS cycle_calibration (
			node_id TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			base_bytes INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (node_id, cycle_start)
		);`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			host TEXT NOT NULL,
			port INTEGER NOT NULL DEFAULT 0,
			probe_port INTEGER NOT NULL DEFAULT 0,
			protocol TEXT NOT NULL DEFAULT '',
			quota_gb INTEGER NOT NULL DEFAULT 0,
			reset_day INTEGER NOT NULL DEFAULT 1,
			expire_date TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init schema failed: %w", err)
		}
	}
	return nil
}

// applyMonotonicDelta calculates non-negative traffic delta even if OS/process counters reset after reboot.
func applyMonotonicDelta(tx *sql.Tx, scope, key string, curRx, curTx int64, now int64) (int64, int64, error) {
	var lastRx, lastTx int64
	err := tx.QueryRow(`SELECT last_rx, last_tx FROM counter_state WHERE scope=? AND key=?`, scope, key).Scan(&lastRx, &lastTx)
	if err == sql.ErrNoRows {
		// First time seeing this counter: store baseline without adding historical boot traffic.
		_, err = tx.Exec(`INSERT INTO counter_state(scope, key, last_rx, last_tx, updated_at) VALUES(?,?,?,?,?)`, scope, key, curRx, curTx, now)
		return 0, 0, err
	}
	if err != nil {
		return 0, 0, err
	}

	var deltaRx, deltaTx int64
	if curRx >= lastRx {
		deltaRx = curRx - lastRx
	} else {
		// Counter reset (e.g., server or service rebooted)
		deltaRx = curRx
	}
	if curTx >= lastTx {
		deltaTx = curTx - lastTx
	} else {
		deltaTx = curTx
	}

	_, err = tx.Exec(`UPDATE counter_state SET last_rx=?, last_tx=?, updated_at=? WHERE scope=? AND key=?`, curRx, curTx, now, scope, key)
	return deltaRx, deltaTx, err
}

func (s *Store) IngestReport(r model.AgentReport) error {
	now := r.Timestamp.Unix()
	if now <= 0 {
		now = time.Now().Unix()
	}
	date := time.Unix(now, 0).Format("2006-01-02")

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if r.NetRxTotal > 0 || r.NetTxTotal > 0 {
		dRx, dTx, err := applyMonotonicDelta(tx, "node_net", r.NodeID, r.NetRxTotal, r.NetTxTotal, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO daily_traffic(node_id, date, rx_bytes, tx_bytes) VALUES(?,?,?,?)
			ON CONFLICT(node_id, date) DO UPDATE SET
				rx_bytes = rx_bytes + excluded.rx_bytes,
				tx_bytes = tx_bytes + excluded.tx_bytes
		`, r.NodeID, date, dRx, dTx); err != nil {
			return err
		}
	}

	var avgLat float64
	var latCount int
	for _, p := range r.Probes {
		upd := p.UpdatedAt.Unix()
		if upd <= 0 {
			upd = now
		}
		if _, err := tx.Exec(`
			INSERT INTO probe_results(node_id, name, tag, latency_ms, loss_pct, status, updated_at)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(node_id, name) DO UPDATE SET
				tag=excluded.tag, latency_ms=excluded.latency_ms, loss_pct=excluded.loss_pct,
				status=excluded.status, updated_at=excluded.updated_at
		`, r.NodeID, p.Name, p.Tag, p.LatencyMs, p.LossPct, p.Status, upd); err != nil {
			return err
		}
		if p.LatencyMs > 0 {
			avgLat += p.LatencyMs
			latCount++
		}
	}
	if latCount > 0 {
		avgLat /= float64(latCount)
	}

	if _, err := tx.Exec(`
		INSERT INTO resource_samples(node_id, ts, cpu_pct, mem_pct, disk_pct, rx_bps, tx_bps, avg_latency_ms, active_conns)
		VALUES(?,?,?,?,?,?,?,?,?)
	`, r.NodeID, now, r.CPUPct, r.MemPct, r.DiskPct, r.RxBps, r.TxBps, avgLat, r.ActiveConns); err != nil {
		return err
	}

	// Mark previously active connections for this node to 0 before updating current active clients
	if _, err := tx.Exec(`UPDATE client_ip_traffic SET active_conns=0, rx_bps=0, tx_bps=0 WHERE node_id=?`, r.NodeID); err != nil {
		return err
	}
	for _, c := range r.Clients {
		var dRx, dTx int64
		if c.SessionRx > 0 || c.SessionTx > 0 {
			var derr error
			dRx, dTx, derr = applyMonotonicDelta(tx, "client_ip:"+r.NodeID, c.IP, c.SessionRx, c.SessionTx, now)
			if derr != nil {
				return derr
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO client_ip_traffic(node_id, ip, active_conns, rx_bps, tx_bps, total_rx, total_tx, last_seen)
			VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(node_id, ip) DO UPDATE SET
				active_conns=excluded.active_conns,
				rx_bps=excluded.rx_bps,
				tx_bps=excluded.tx_bps,
				total_rx=total_rx + excluded.total_rx,
				total_tx=total_tx + excluded.total_tx,
				last_seen=excluded.last_seen
		`, r.NodeID, c.IP, c.ActiveConns, c.RxBps, c.TxBps, dRx, dTx, now); err != nil {
			return err
		}
	}

	// Keep samples within 8 days
	cutoff := now - 8*86400
	_, _ = tx.Exec(`DELETE FROM resource_samples WHERE ts < ?`, cutoff)

	return tx.Commit()
}

func (s *Store) SaveProbes(nodeID string, probes []model.ProbeEntry) error {
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range probes {
		upd := p.UpdatedAt.Unix()
		if upd <= 0 {
			upd = now
		}
		if _, err := tx.Exec(`
			INSERT INTO probe_results(node_id, name, tag, latency_ms, loss_pct, status, updated_at)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(node_id, name) DO UPDATE SET
				tag=excluded.tag, latency_ms=excluded.latency_ms, loss_pct=excluded.loss_pct,
				status=excluded.status, updated_at=excluded.updated_at
		`, nodeID, p.Name, p.Tag, p.LatencyMs, p.LossPct, p.Status, upd); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetDailyTraffic(nodeID string, days int) ([]model.DailyTraffic, error) {
	startDate := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	var rows *sql.Rows
	var err error
	if nodeID == "" {
		rows, err = s.db.Query(`
			SELECT date, SUM(rx_bytes), SUM(tx_bytes)
			FROM daily_traffic WHERE date >= ?
			GROUP BY date ORDER BY date ASC`, startDate)
	} else {
		rows, err = s.db.Query(`
			SELECT date, rx_bytes, tx_bytes
			FROM daily_traffic WHERE node_id = ? AND date >= ?
			ORDER BY date ASC`, nodeID, startDate)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDate := make(map[string]model.DailyTraffic)
	for rows.Next() {
		var d model.DailyTraffic
		if err := rows.Scan(&d.Date, &d.RxBytes, &d.TxBytes); err != nil {
			return nil, err
		}
		byDate[d.Date] = d
	}

	out := make([]model.DailyTraffic, 0, days)
	for i := days - 1; i >= 0; i-- {
		dt := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		if v, ok := byDate[dt]; ok {
			out = append(out, v)
		} else {
			out = append(out, model.DailyTraffic{Date: dt})
		}
	}
	return out, nil
}

func (s *Store) GetTrafficSince(nodeID, startDate string) (rx int64, tx int64, err error) {
	if nodeID == "" {
		err = s.db.QueryRow(`SELECT COALESCE(SUM(rx_bytes),0), COALESCE(SUM(tx_bytes),0) FROM daily_traffic WHERE date >= ?`, startDate).Scan(&rx, &tx)
	} else {
		err = s.db.QueryRow(`SELECT COALESCE(SUM(rx_bytes),0), COALESCE(SUM(tx_bytes),0) FROM daily_traffic WHERE node_id = ? AND date >= ?`, nodeID, startDate).Scan(&rx, &tx)
	}
	return
}

func (s *Store) GetResourceSamples(nodeID string, since time.Time) ([]model.ResourceSample, error) {
	var rows *sql.Rows
	var err error
	if nodeID == "" {
		rows, err = s.db.Query(`
			SELECT ts, AVG(cpu_pct), AVG(mem_pct), AVG(disk_pct), SUM(rx_bps), SUM(tx_bps), AVG(avg_latency_ms), SUM(active_conns)
			FROM resource_samples WHERE ts >= ?
			GROUP BY ts/60 ORDER BY ts ASC`, since.Unix())
	} else {
		rows, err = s.db.Query(`
			SELECT ts, cpu_pct, mem_pct, disk_pct, rx_bps, tx_bps, avg_latency_ms, active_conns
			FROM resource_samples WHERE node_id = ? AND ts >= ?
			ORDER BY ts ASC`, nodeID, since.Unix())
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.ResourceSample
	for rows.Next() {
		var item model.ResourceSample
		var ts int64
		if err := rows.Scan(&ts, &item.CPUPct, &item.MemPct, &item.DiskPct, &item.RxBps, &item.TxBps, &item.AvgLatencyMs, &item.ActiveConns); err != nil {
			return nil, err
		}
		item.Timestamp = time.Unix(ts, 0)
		out = append(out, item)
	}
	return out, nil
}

func (s *Store) GetProbes(nodeID string) ([]model.ProbeEntry, error) {
	rows, err := s.db.Query(`SELECT name, tag, latency_ms, loss_pct, status, updated_at FROM probe_results WHERE node_id=? ORDER BY tag, name`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProbeEntry
	for rows.Next() {
		var p model.ProbeEntry
		var ts int64
		if err := rows.Scan(&p.Name, &p.Tag, &p.LatencyMs, &p.LossPct, &p.Status, &ts); err != nil {
			return nil, err
		}
		p.UpdatedAt = time.Unix(ts, 0)
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) GetClients(nodeID string, limit int) ([]model.ClientIPStat, error) {
	var rows *sql.Rows
	var err error
	if nodeID == "" {
		rows, err = s.db.Query(`
			SELECT node_id, ip, active_conns, rx_bps, tx_bps, total_rx, total_tx, last_seen
			FROM client_ip_traffic
			ORDER BY active_conns DESC, (total_rx+total_tx) DESC, last_seen DESC
			LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`
			SELECT node_id, ip, active_conns, rx_bps, tx_bps, total_rx, total_tx, last_seen
			FROM client_ip_traffic WHERE node_id=?
			ORDER BY active_conns DESC, (total_rx+total_tx) DESC, last_seen DESC
			LIMIT ?`, nodeID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.ClientIPStat
	for rows.Next() {
		var c model.ClientIPStat
		var ts int64
		if err := rows.Scan(&c.NodeID, &c.IP, &c.ActiveConns, &c.RxBps, &c.TxBps, &c.TotalRx, &c.TotalTx, &ts); err != nil {
			return nil, err
		}
		c.LastSeen = time.Unix(ts, 0)
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) GetGeo(ip string) (string, bool) {
	var reg string
	err := s.db.QueryRow(`SELECT region FROM ip_geo_cache WHERE ip=?`, ip).Scan(&reg)
	if err != nil {
		return "", false
	}
	return reg, true
}

func (s *Store) SaveGeo(ip, region string) error {
	_, err := s.db.Exec(`
		INSERT INTO ip_geo_cache(ip, region, updated_at) VALUES(?,?,?)
		ON CONFLICT(ip) DO UPDATE SET region=excluded.region, updated_at=excluded.updated_at
	`, ip, region, time.Now().Unix())
	return err
}

func (s *Store) SetCycleCalibration(nodeID, cycleStart string, baseBytes int64) error {
	_, err := s.db.Exec(`
		INSERT INTO cycle_calibration(node_id, cycle_start, base_bytes, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(node_id, cycle_start) DO UPDATE SET base_bytes=excluded.base_bytes, updated_at=excluded.updated_at
	`, nodeID, cycleStart, baseBytes, time.Now().Unix())
	return err
}

func (s *Store) UpsertNode(n model.NodeDef, overwrite bool) error {
	if n.ID == "" {
		return nil
	}
	if n.Name == "" {
		n.Name = n.ID
	}
	if n.ResetDay <= 0 || n.ResetDay > 28 {
		n.ResetDay = 1
	}
	if overwrite {
		_, err := s.db.Exec(`
			INSERT INTO nodes(id, name, host, port, probe_port, protocol, quota_gb, reset_day, expire_date, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, host=excluded.host, port=excluded.port, probe_port=excluded.probe_port,
				protocol=excluded.protocol, quota_gb=excluded.quota_gb, reset_day=excluded.reset_day, expire_date=excluded.expire_date
		`, n.ID, n.Name, n.Host, n.Port, n.ProbePort, n.Protocol, n.QuotaGB, n.ResetDay, n.ExpireDate, time.Now().Unix())
		return err
	}
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO nodes(id, name, host, port, probe_port, protocol, quota_gb, reset_day, expire_date, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
	`, n.ID, n.Name, n.Host, n.Port, n.ProbePort, n.Protocol, n.QuotaGB, n.ResetDay, n.ExpireDate, time.Now().Unix())
	return err
}

func (s *Store) ListNodes() ([]model.NodeDef, error) {
	rows, err := s.db.Query(`SELECT id, name, host, port, probe_port, protocol, quota_gb, reset_day, expire_date FROM nodes ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodeDef
	for rows.Next() {
		var n model.NodeDef
		if err := rows.Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.ProbePort, &n.Protocol, &n.QuotaGB, &n.ResetDay, &n.ExpireDate); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Store) GetCycleCalibration(nodeID, cycleStart string) int64 {
	var b int64
	_ = s.db.QueryRow(`SELECT base_bytes FROM cycle_calibration WHERE node_id=? AND cycle_start=?`, nodeID, cycleStart).Scan(&b)
	return b
}
