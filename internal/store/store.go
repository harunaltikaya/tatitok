// Package store owns the SQLite database: open/migrate, idempotent event
// inserts, the ingested-sources bookkeeping table, and the M1 queries.
// Pure-Go driver (modernc.org/sqlite) — the binary must build CGO_ENABLED=0.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/harunaltikaya/tatitok/internal/core"
	"github.com/harunaltikaya/tatitok/internal/modelmap"
)

// Store wraps the SQLite handle after migration.
type Store struct {
	db *sql.DB
	// quietReplace turns the per-event AS-4 replacement log line off
	// for this handle (see QuietReplacements).
	quietReplace bool
}

// migrations run in order inside one transaction each; schema_version
// records the last applied index + 1. Append-only — never edit an entry
// that has shipped.
var migrations = []string{
	`CREATE TABLE usage_events (
		id                 TEXT PRIMARY KEY,
		ts                 TEXT NOT NULL,            -- RFC3339 UTC
		machine            TEXT NOT NULL,
		source_kind        TEXT NOT NULL,
		harness            TEXT,
		provider           TEXT NOT NULL,
		model              TEXT NOT NULL,
		model_family       TEXT NOT NULL,
		project            TEXT,
		session_id         TEXT,
		request_id         TEXT,
		tokens_input       INTEGER NOT NULL,
		tokens_output      INTEGER NOT NULL,
		tokens_cache_write INTEGER NOT NULL,
		tokens_cache_read  INTEGER NOT NULL,
		tokens_reasoning   INTEGER,
		accuracy           TEXT NOT NULL CHECK (accuracy IN ('exact','derived','estimated')),
		meta               TEXT,                     -- JSON
		raw                TEXT                      -- sanitized JSON
	);
	CREATE INDEX idx_events_ts          ON usage_events (ts);
	CREATE INDEX idx_events_harness_ts  ON usage_events (harness, ts);
	CREATE INDEX idx_events_model_ts    ON usage_events (model, ts);
	CREATE INDEX idx_events_project_ts  ON usage_events (project, ts);

	CREATE TABLE sources (
		path        TEXT PRIMARY KEY,                -- absolute source file path
		harness     TEXT NOT NULL,
		mtime       TEXT NOT NULL,                   -- RFC3339 UTC
		size        INTEGER NOT NULL,
		line_count  INTEGER NOT NULL,
		ingested_at TEXT NOT NULL,                   -- RFC3339 UTC
		parse_errors INTEGER NOT NULL DEFAULT 0
	);`,
	// read_error: why this source was skipped (NULL = read fully); a
	// later successful ingest of the same path clears it via the upsert.
	`ALTER TABLE sources ADD COLUMN read_error TEXT;`,
	// incomplete_tail: the file's final line was unterminated and did not
	// parse (write in progress, not a parse error); cleared by the next
	// backfill of the same path via the upsert.
	`ALTER TABLE sources ADD COLUMN incomplete_tail INTEGER NOT NULL DEFAULT 0;`,
	// AdapterVersion provenance (M2 Task 0): which adapter format-handling
	// version produced each stored event and each ingested source file.
	// The adapter NAME is the existing harness column on both tables. NULL
	// on rows ingested before this migration.
	`ALTER TABLE usage_events ADD COLUMN adapter_version INTEGER;
	ALTER TABLE sources ADD COLUMN adapter_version INTEGER;`,
	// Source lineage (M3 Task 0, migration 5). sources gains a stable
	// source_id — core.SourceID(harness, path), exposed to SQL as
	// tatitok_source_id — and the collecting machine; usage_events gains a
	// source_id FK (deferred: a file's events insert before its sources row
	// lands at commit) so stale events are identifiable per file for
	// recompute and rollup corrections.
	//
	// Backfill: every sources row gets its deterministic source_id; machine
	// fills where the harness has exactly one distinct machine across its
	// events. Existing events link to a source by path match where
	// UNAMBIGUOUS — the session id appears in exactly one recorded source
	// path (claude-code and codex embed it in the filename; codex rollout
	// backup copies match twice and stay NULL on purpose), else the harness
	// has exactly one recorded source (opencode's single db file).
	// Everything else stays NULL and is counted (migration hook log);
	// `tatitok recompute --provenance` completes those precisely by ID.
	`ALTER TABLE sources ADD COLUMN machine TEXT;
	ALTER TABLE sources ADD COLUMN source_id TEXT;
	UPDATE sources SET source_id = tatitok_source_id(harness, path);
	CREATE UNIQUE INDEX idx_sources_source_id ON sources (source_id);
	ALTER TABLE usage_events ADD COLUMN source_id TEXT
		REFERENCES sources (source_id) DEFERRABLE INITIALLY DEFERRED;
	UPDATE sources SET machine = (
		SELECT MIN(e.machine) FROM usage_events e WHERE e.harness = sources.harness
	) WHERE (
		SELECT COUNT(DISTINCT e.machine) FROM usage_events e WHERE e.harness = sources.harness
	) = 1;
	CREATE TEMP TABLE lineage_match AS
		SELECT e.harness AS harness, e.session_id AS session_id,
		       MIN(s.source_id) AS source_id,
		       COUNT(DISTINCT s.source_id) AS n
		FROM (SELECT DISTINCT harness, session_id FROM usage_events
		      WHERE harness IS NOT NULL
		        AND session_id IS NOT NULL AND session_id <> '') AS e
		JOIN sources s ON s.harness = e.harness
		              AND instr(s.path, e.session_id) > 0
		GROUP BY e.harness, e.session_id;
	UPDATE usage_events SET source_id = (
		SELECT m.source_id FROM lineage_match m
		WHERE m.harness = usage_events.harness
		  AND m.session_id = usage_events.session_id AND m.n = 1
	) WHERE source_id IS NULL AND EXISTS (
		SELECT 1 FROM lineage_match m
		WHERE m.harness = usage_events.harness
		  AND m.session_id = usage_events.session_id AND m.n = 1
	);
	DROP TABLE lineage_match;
	UPDATE usage_events SET source_id = (
		SELECT MIN(s.source_id) FROM sources s WHERE s.harness = usage_events.harness
	) WHERE source_id IS NULL AND (
		SELECT COUNT(*) FROM sources s WHERE s.harness = usage_events.harness
	) = 1;
	CREATE INDEX idx_events_source ON usage_events (source_id);`,
	// Versioned model normalization (M3 Task 1, migration 6). model_map
	// mirrors the embedded seed (internal/modelmap, synced at Open);
	// usage_events.map_version records which map normalized each row's
	// model_family. NO data backfill here: existing rows keep
	// model_family = model and a NULL map_version until the owner runs
	// `tatitok recompute --model-map` — historical model_family changes
	// are ONLY ever explicit (AS-4).
	`ALTER TABLE usage_events ADD COLUMN map_version INTEGER;
	CREATE TABLE model_map (
		model        TEXT PRIMARY KEY,
		model_family TEXT NOT NULL,
		map_version  INTEGER NOT NULL
	);`,
	// Pricing engine (M3 Task 2, migration 7). Four derived cost columns,
	// integer micro-USD only (no REAL anywhere): cost_usd_micro is NULL
	// exactly when the event could not be priced; cost_basis per PRD §9.1
	// (M3 uses 'local' — the energy model is a later milestone);
	// price_snapshot + price_rates pin what priced each row (FR-9.5), so
	// a snapshot refresh never changes historical costs silently — that
	// is `tatitok recompute --pricing`, explicit. NO data backfill here:
	// pre-pricing rows stay NULL until that recompute.
	`ALTER TABLE usage_events ADD COLUMN cost_usd_micro INTEGER;
	ALTER TABLE usage_events ADD COLUMN cost_basis TEXT
		CHECK (cost_basis IN ('api_price','plan_included','local','free','unknown'));
	ALTER TABLE usage_events ADD COLUMN price_snapshot TEXT;
	ALTER TABLE usage_events ADD COLUMN price_rates TEXT;`,
	// Owner ruling 2026-06-11 (migration 8): free-basis events (source
	// reported exactly $0) bill 0 but ALWAYS carry the computed
	// API-equivalent value when the snapshot can price the model —
	// mirroring the local-basis design (FR-9.3). Derived column.
	`ALTER TABLE usage_events ADD COLUMN cost_api_equiv_micro INTEGER;`,
	// Rollups (M3 Task 3, migration 9). rollup_daily is maintained by
	// TRIGGERS inside every write transaction — the one mechanism that
	// stays correct through ingest inserts, mutable-store replacements
	// (subtract old, add new) AND the explicit recomputes that move rows
	// between buckets (model-map changes model_family, pricing changes
	// cost). Decisions recorded for the milestone report:
	//   - day_utc = substr(ts,1,10): rollups bucket in UTC; non-UTC
	//     timezone queries keep exact event-level aggregation (a UTC day
	//     cannot serve a :30/:45-offset zone); hourly grain DEFERRED to
	//     the live milestone.
	//   - raw model joins the grain (and the PK, with model_family —
	//     mixed-map states are real) so the rollup-served stats path is
	//     byte-equal to direct aggregation.
	//   - map_version/snapshot_version: last-written here, superseded by
	//     migration 10's MAX semantics (advisory; exact after
	//     `recompute --rollups`).
	// The backfill at the end constructs rollups for pre-existing events
	// (new derived data — no historical numbers change).
	`CREATE TABLE rollup_daily (
		day_utc              TEXT NOT NULL,
		machine              TEXT NOT NULL,
		harness              TEXT NOT NULL,
		provider             TEXT NOT NULL,
		model                TEXT NOT NULL,
		model_family         TEXT NOT NULL,
		project              TEXT NOT NULL,
		events               INTEGER NOT NULL DEFAULT 0,
		tokens_input         INTEGER NOT NULL DEFAULT 0,
		tokens_output        INTEGER NOT NULL DEFAULT 0,
		tokens_cache_write   INTEGER NOT NULL DEFAULT 0,
		tokens_cache_read    INTEGER NOT NULL DEFAULT 0,
		tokens_reasoning     INTEGER NOT NULL DEFAULT 0,
		cost_usd_micro       INTEGER NOT NULL DEFAULT 0,
		cost_api_equiv_micro INTEGER NOT NULL DEFAULT 0,
		events_unpriced      INTEGER NOT NULL DEFAULT 0,
		map_version          INTEGER,
		snapshot_version     TEXT,
		PRIMARY KEY (day_utc, machine, harness, provider, model, model_family, project)
	) WITHOUT ROWID;
	CREATE TRIGGER rollup_daily_ai AFTER INSERT ON usage_events BEGIN
		` + rollupAddNewSQL + `
	END;
	CREATE TRIGGER rollup_daily_au AFTER UPDATE ON usage_events BEGIN
		` + rollupSubtractOldSQL + `
		` + rollupAddNewSQL + `
		` + rollupCleanupOldSQL + `
	END;
	CREATE TRIGGER rollup_daily_ad AFTER DELETE ON usage_events BEGIN
		` + rollupSubtractOldSQL + `
		` + rollupCleanupOldSQL + `
	END;
	` + rollupRebuildSQL,
	// Rollup version columns (M3.1 finding 4, migration 10). Owner ruling:
	// rollup map_version/snapshot_version are ADVISORY — MAX over the
	// contributing events' versions, exact after `recompute --rollups`.
	// Migration 9's triggers kept the LAST contributing event's versions,
	// which diverged from the rebuild's MAX under out-of-order ingest; the
	// recreated insert/update triggers use MAX semantics, aligning the
	// incremental path with rollupRebuildSQL. A delete or bucket-move can
	// still leave a stale MAX until the explicit rebuild — that is the
	// advisory contract. The delete trigger never touched version columns
	// and is unchanged.
	`DROP TRIGGER rollup_daily_ai;
	DROP TRIGGER rollup_daily_au;
	CREATE TRIGGER rollup_daily_ai AFTER INSERT ON usage_events BEGIN
		` + rollupAddNewMaxSQL + `
	END;
	CREATE TRIGGER rollup_daily_au AFTER UPDATE ON usage_events BEGIN
		` + rollupSubtractOldSQL + `
		` + rollupAddNewMaxSQL + `
		` + rollupCleanupOldSQL + `
	END;`,
	// Hourly rollups (M6 Task 1, migration 11). The hour grain mirrors
	// rollup_daily exactly — same dimensional PK, same MAX-semantics
	// version columns, the same trigger discipline inside every write
	// transaction — bucketed on the UTC hour (substr(ts,1,13) =
	// "YYYY-MM-DDTHH") instead of the UTC day. It exists to SERVE
	// (whole-hour-offset local days, M6 Task 2), never to chart.
	//
	// The conservation law of the grain: every rollup_daily row equals
	// the sum of its (≤24) rollup_hourly rows for the same dimension
	// combination, byte-equal on the additive measures —
	// substr(hour_utc,1,10) = day_utc by construction. The triggers
	// carry MAX version semantics from the start (no migration-9→10
	// replay), so the incremental path agrees with the rebuild's MAX();
	// version columns stay advisory (M3.1), exact after
	// `+"`recompute --rollups`"+`. VerifyRollupConservation asserts the
	// law against the live DB; the rollup property tests pin it over
	// fixtures and randomized trigger paths.
	//
	// The backfill at the end constructs hourly rollups for pre-existing
	// events (new derived data — no historical numbers change), exactly
	// as migration 9 did for the day grain.
	`CREATE TABLE rollup_hourly (
		hour_utc             TEXT NOT NULL,
		machine              TEXT NOT NULL,
		harness              TEXT NOT NULL,
		provider             TEXT NOT NULL,
		model                TEXT NOT NULL,
		model_family         TEXT NOT NULL,
		project              TEXT NOT NULL,
		events               INTEGER NOT NULL DEFAULT 0,
		tokens_input         INTEGER NOT NULL DEFAULT 0,
		tokens_output        INTEGER NOT NULL DEFAULT 0,
		tokens_cache_write   INTEGER NOT NULL DEFAULT 0,
		tokens_cache_read    INTEGER NOT NULL DEFAULT 0,
		tokens_reasoning     INTEGER NOT NULL DEFAULT 0,
		cost_usd_micro       INTEGER NOT NULL DEFAULT 0,
		cost_api_equiv_micro INTEGER NOT NULL DEFAULT 0,
		events_unpriced      INTEGER NOT NULL DEFAULT 0,
		map_version          INTEGER,
		snapshot_version     TEXT,
		PRIMARY KEY (hour_utc, machine, harness, provider, model, model_family, project)
	) WITHOUT ROWID;
	CREATE TRIGGER rollup_hourly_ai AFTER INSERT ON usage_events BEGIN
		` + rollupHourlyAddNewMaxSQL + `
	END;
	CREATE TRIGGER rollup_hourly_au AFTER UPDATE ON usage_events BEGIN
		` + rollupHourlySubtractOldSQL + `
		` + rollupHourlyAddNewMaxSQL + `
		` + rollupHourlyCleanupOldSQL + `
	END;
	CREATE TRIGGER rollup_hourly_ad AFTER DELETE ON usage_events BEGIN
		` + rollupHourlySubtractOldSQL + `
		` + rollupHourlyCleanupOldSQL + `
	END;
	` + rollupHourlyRebuildSQL,
}

// rollupKeyOld matches a rollup row by the OLD event values (NULL-safe
// COALESCE matching the insert path).
const rollupKeyOld = `day_utc = substr(old.ts, 1, 10) AND machine = old.machine
	AND harness = COALESCE(old.harness, '') AND provider = old.provider
	AND model = old.model AND model_family = old.model_family
	AND project = COALESCE(old.project, '')`

// rollupAddNewSQL upserts the NEW event row's contribution. FROZEN for
// shipped migration 9 (append-only rule): its last-written version
// columns were superseded by rollupAddNewMaxSQL in migration 10.
const rollupAddNewSQL = `INSERT INTO rollup_daily (day_utc, machine, harness,
		provider, model, model_family, project, events, tokens_input,
		tokens_output, tokens_cache_write, tokens_cache_read,
		tokens_reasoning, cost_usd_micro, cost_api_equiv_micro,
		events_unpriced, map_version, snapshot_version)
	VALUES (substr(new.ts, 1, 10), new.machine, COALESCE(new.harness, ''),
		new.provider, new.model, new.model_family, COALESCE(new.project, ''),
		1, new.tokens_input, new.tokens_output, new.tokens_cache_write,
		new.tokens_cache_read, COALESCE(new.tokens_reasoning, 0),
		COALESCE(new.cost_usd_micro, 0), COALESCE(new.cost_api_equiv_micro, 0),
		(new.cost_usd_micro IS NULL), new.map_version, new.price_snapshot)
	ON CONFLICT (day_utc, machine, harness, provider, model, model_family, project)
	DO UPDATE SET
		events = events + 1,
		tokens_input = tokens_input + new.tokens_input,
		tokens_output = tokens_output + new.tokens_output,
		tokens_cache_write = tokens_cache_write + new.tokens_cache_write,
		tokens_cache_read = tokens_cache_read + new.tokens_cache_read,
		tokens_reasoning = tokens_reasoning + COALESCE(new.tokens_reasoning, 0),
		cost_usd_micro = cost_usd_micro + COALESCE(new.cost_usd_micro, 0),
		cost_api_equiv_micro = cost_api_equiv_micro + COALESCE(new.cost_api_equiv_micro, 0),
		events_unpriced = events_unpriced + (new.cost_usd_micro IS NULL),
		map_version = COALESCE(new.map_version, map_version),
		snapshot_version = COALESCE(new.price_snapshot, snapshot_version);`

// rollupAddNewMaxSQL is migration 10's add-new upsert: identical to
// rollupAddNewSQL except the version columns take MAX over the bucket's
// contributors (NULL-safe — sqlite scalar max() is NULL when either arg
// is), matching rollupRebuildSQL's MAX() aggregates. Advisory by ruling:
// exact after `recompute --rollups`.
const rollupAddNewMaxSQL = `INSERT INTO rollup_daily (day_utc, machine, harness,
		provider, model, model_family, project, events, tokens_input,
		tokens_output, tokens_cache_write, tokens_cache_read,
		tokens_reasoning, cost_usd_micro, cost_api_equiv_micro,
		events_unpriced, map_version, snapshot_version)
	VALUES (substr(new.ts, 1, 10), new.machine, COALESCE(new.harness, ''),
		new.provider, new.model, new.model_family, COALESCE(new.project, ''),
		1, new.tokens_input, new.tokens_output, new.tokens_cache_write,
		new.tokens_cache_read, COALESCE(new.tokens_reasoning, 0),
		COALESCE(new.cost_usd_micro, 0), COALESCE(new.cost_api_equiv_micro, 0),
		(new.cost_usd_micro IS NULL), new.map_version, new.price_snapshot)
	ON CONFLICT (day_utc, machine, harness, provider, model, model_family, project)
	DO UPDATE SET
		events = events + 1,
		tokens_input = tokens_input + new.tokens_input,
		tokens_output = tokens_output + new.tokens_output,
		tokens_cache_write = tokens_cache_write + new.tokens_cache_write,
		tokens_cache_read = tokens_cache_read + new.tokens_cache_read,
		tokens_reasoning = tokens_reasoning + COALESCE(new.tokens_reasoning, 0),
		cost_usd_micro = cost_usd_micro + COALESCE(new.cost_usd_micro, 0),
		cost_api_equiv_micro = cost_api_equiv_micro + COALESCE(new.cost_api_equiv_micro, 0),
		events_unpriced = events_unpriced + (new.cost_usd_micro IS NULL),
		map_version = COALESCE(MAX(map_version, new.map_version), map_version, new.map_version),
		snapshot_version = COALESCE(MAX(snapshot_version, new.price_snapshot), snapshot_version, new.price_snapshot);`

// rollupSubtractOldSQL removes the OLD event row's contribution.
const rollupSubtractOldSQL = `UPDATE rollup_daily SET
		events = events - 1,
		tokens_input = tokens_input - old.tokens_input,
		tokens_output = tokens_output - old.tokens_output,
		tokens_cache_write = tokens_cache_write - old.tokens_cache_write,
		tokens_cache_read = tokens_cache_read - old.tokens_cache_read,
		tokens_reasoning = tokens_reasoning - COALESCE(old.tokens_reasoning, 0),
		cost_usd_micro = cost_usd_micro - COALESCE(old.cost_usd_micro, 0),
		cost_api_equiv_micro = cost_api_equiv_micro - COALESCE(old.cost_api_equiv_micro, 0),
		events_unpriced = events_unpriced - (old.cost_usd_micro IS NULL)
	WHERE ` + rollupKeyOld + `;`

// rollupCleanupOldSQL drops the old bucket when it emptied, so the
// incremental table stays identical to a fresh rebuild.
const rollupCleanupOldSQL = `DELETE FROM rollup_daily
	WHERE events = 0 AND ` + rollupKeyOld + `;`

// rollupRebuildSQL constructs rollup_daily from usage_events — used by
// the migration backfill and by `recompute --rollups` (after a DELETE).
const rollupRebuildSQL = `INSERT INTO rollup_daily (day_utc, machine, harness,
		provider, model, model_family, project, events, tokens_input,
		tokens_output, tokens_cache_write, tokens_cache_read,
		tokens_reasoning, cost_usd_micro, cost_api_equiv_micro,
		events_unpriced, map_version, snapshot_version)
	SELECT substr(ts, 1, 10), machine, COALESCE(harness, ''), provider,
		model, model_family, COALESCE(project, ''),
		COUNT(*), SUM(tokens_input), SUM(tokens_output),
		SUM(tokens_cache_write), SUM(tokens_cache_read),
		SUM(COALESCE(tokens_reasoning, 0)), SUM(COALESCE(cost_usd_micro, 0)),
		SUM(COALESCE(cost_api_equiv_micro, 0)), SUM(cost_usd_micro IS NULL),
		MAX(map_version), MAX(price_snapshot)
	FROM usage_events
	GROUP BY 1, 2, 3, 4, 5, 6, 7;`

// The hour-grain rollup SQL (M6 Task 1) mirrors the day-grain constants
// above verbatim except for the table (rollup_hourly), the bucket column
// (hour_utc) and the bucket expression (substr(ts,1,13) = the UTC hour
// "YYYY-MM-DDTHH" vs substr(ts,1,10) = the UTC day). Kept as parallel
// literals — not generated — so the trigger bodies read identically to
// the day grain a reviewer already trusts; VerifyRollupConservation and
// the rollup property tests are the guarantee the two grains agree.

// rollupHourlyKeyOld matches a rollup_hourly row by the OLD event values.
const rollupHourlyKeyOld = `hour_utc = substr(old.ts, 1, 13) AND machine = old.machine
	AND harness = COALESCE(old.harness, '') AND provider = old.provider
	AND model = old.model AND model_family = old.model_family
	AND project = COALESCE(old.project, '')`

// rollupHourlyAddNewMaxSQL upserts the NEW event's contribution with MAX
// version semantics (the day grain's migration-10 form, from the start).
const rollupHourlyAddNewMaxSQL = `INSERT INTO rollup_hourly (hour_utc, machine, harness,
		provider, model, model_family, project, events, tokens_input,
		tokens_output, tokens_cache_write, tokens_cache_read,
		tokens_reasoning, cost_usd_micro, cost_api_equiv_micro,
		events_unpriced, map_version, snapshot_version)
	VALUES (substr(new.ts, 1, 13), new.machine, COALESCE(new.harness, ''),
		new.provider, new.model, new.model_family, COALESCE(new.project, ''),
		1, new.tokens_input, new.tokens_output, new.tokens_cache_write,
		new.tokens_cache_read, COALESCE(new.tokens_reasoning, 0),
		COALESCE(new.cost_usd_micro, 0), COALESCE(new.cost_api_equiv_micro, 0),
		(new.cost_usd_micro IS NULL), new.map_version, new.price_snapshot)
	ON CONFLICT (hour_utc, machine, harness, provider, model, model_family, project)
	DO UPDATE SET
		events = events + 1,
		tokens_input = tokens_input + new.tokens_input,
		tokens_output = tokens_output + new.tokens_output,
		tokens_cache_write = tokens_cache_write + new.tokens_cache_write,
		tokens_cache_read = tokens_cache_read + new.tokens_cache_read,
		tokens_reasoning = tokens_reasoning + COALESCE(new.tokens_reasoning, 0),
		cost_usd_micro = cost_usd_micro + COALESCE(new.cost_usd_micro, 0),
		cost_api_equiv_micro = cost_api_equiv_micro + COALESCE(new.cost_api_equiv_micro, 0),
		events_unpriced = events_unpriced + (new.cost_usd_micro IS NULL),
		map_version = COALESCE(MAX(map_version, new.map_version), map_version, new.map_version),
		snapshot_version = COALESCE(MAX(snapshot_version, new.price_snapshot), snapshot_version, new.price_snapshot);`

// rollupHourlySubtractOldSQL removes the OLD event's contribution.
const rollupHourlySubtractOldSQL = `UPDATE rollup_hourly SET
		events = events - 1,
		tokens_input = tokens_input - old.tokens_input,
		tokens_output = tokens_output - old.tokens_output,
		tokens_cache_write = tokens_cache_write - old.tokens_cache_write,
		tokens_cache_read = tokens_cache_read - old.tokens_cache_read,
		tokens_reasoning = tokens_reasoning - COALESCE(old.tokens_reasoning, 0),
		cost_usd_micro = cost_usd_micro - COALESCE(old.cost_usd_micro, 0),
		cost_api_equiv_micro = cost_api_equiv_micro - COALESCE(old.cost_api_equiv_micro, 0),
		events_unpriced = events_unpriced - (old.cost_usd_micro IS NULL)
	WHERE ` + rollupHourlyKeyOld + `;`

// rollupHourlyCleanupOldSQL drops the old hour bucket when it emptied.
const rollupHourlyCleanupOldSQL = `DELETE FROM rollup_hourly
	WHERE events = 0 AND ` + rollupHourlyKeyOld + `;`

// rollupHourlyRebuildSQL constructs rollup_hourly from usage_events — the
// migration-11 backfill and `recompute --rollups`.
const rollupHourlyRebuildSQL = `INSERT INTO rollup_hourly (hour_utc, machine, harness,
		provider, model, model_family, project, events, tokens_input,
		tokens_output, tokens_cache_write, tokens_cache_read,
		tokens_reasoning, cost_usd_micro, cost_api_equiv_micro,
		events_unpriced, map_version, snapshot_version)
	SELECT substr(ts, 1, 13), machine, COALESCE(harness, ''), provider,
		model, model_family, COALESCE(project, ''),
		COUNT(*), SUM(tokens_input), SUM(tokens_output),
		SUM(tokens_cache_write), SUM(tokens_cache_read),
		SUM(COALESCE(tokens_reasoning, 0)), SUM(COALESCE(cost_usd_micro, 0)),
		SUM(COALESCE(cost_api_equiv_micro, 0)), SUM(cost_usd_micro IS NULL),
		MAX(map_version), MAX(price_snapshot)
	FROM usage_events
	GROUP BY 1, 2, 3, 4, 5, 6, 7;`

// migrationHooks run inside the migration's transaction, after its SQL —
// used to surface backfill outcomes (a migration must never be silent
// about data it could not link). Keyed by migration index.
var migrationHooks = map[int]func(*sql.Tx) error{
	4: reportLineageBackfill,
}

// reportLineageBackfill logs what migration 5 linked and what it left
// NULL ("otherwise NULL + counted" — milestone-3 Task 0).
func reportLineageBackfill(tx *sql.Tx) error {
	var events, unlinked, srcNoMachine int64
	if err := tx.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(source_id IS NULL), 0) FROM usage_events`).
		Scan(&events, &unlinked); err != nil {
		return fmt.Errorf("lineage backfill report: %w", err)
	}
	if err := tx.QueryRow(`SELECT COALESCE(SUM(machine IS NULL), 0) FROM sources`).
		Scan(&srcNoMachine); err != nil {
		return fmt.Errorf("lineage backfill report: %w", err)
	}
	if events == 0 && srcNoMachine == 0 {
		return nil // fresh database — nothing to backfill
	}
	slog.Info("migration 5: source lineage backfill",
		"events", events,
		"events_linked", events-unlinked,
		"events_without_source_link", unlinked,
		"sources_without_machine", srcNoMachine,
		"next", "tatitok recompute --provenance completes NULL rows from the source files")
	return nil
}

// dsn builds the connection string. _pragma values apply per connection;
// busy_timeout guards WAL writers. _txlock=immediate makes every explicit
// transaction take the write lock at BEGIN: all of this package's
// transactions are writers, and a deferred BEGIN that upgrades to a write
// lock mid-transaction can deadlock or interleave with a concurrent
// writer (two Opens migrating, recompute racing an ingest replacement).
// busy_timeout is FIRST in the pragma list deliberately (M4 Codex
// round, finding 1): pragmas apply in order at connection setup, and
// journal_mode(WAL) on a fresh database is itself a locking write — two
// connections racing the WAL conversion before any busy handler exists
// fail instantly with SQLITE_BUSY. With the timeout set first, every
// later pragma and statement waits like any other writer.
func dsn(path string) string {
	return fmt.Sprintf("file:%s?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
}

// Open opens (creating if needed) the database at path, enables WAL, and
// applies pending migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := syncModelMap(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// syncModelMap mirrors the embedded seed into the model_map table when
// the stored version differs. This refreshes only the LOOKUP table —
// stored events keep the model_family/map_version they were stamped
// with until `tatitok recompute --model-map` (AS-4: re-normalization is
// never a side effect of opening the database).
func syncModelMap(db *sql.DB) error {
	var stored sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(map_version) FROM model_map`).Scan(&stored); err != nil {
		return fmt.Errorf("model_map version: %w", err)
	}
	cur := int64(modelmap.Version())
	if stored.Valid && stored.Int64 == cur {
		return nil
	}
	tx, err := beginWrite(db)
	if err != nil {
		return fmt.Errorf("begin model_map sync: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM model_map`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear model_map: %w", err)
	}
	for _, e := range modelmap.Entries() {
		if _, err := tx.Exec(`INSERT INTO model_map (model, model_family, map_version)
			VALUES (?, ?, ?)`, e.Model, e.Family, cur); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("seed model_map %q: %w", e.Model, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model_map sync: %w", err)
	}
	if stored.Valid {
		slog.Info("model_map table refreshed from embedded seed",
			"from_version", stored.Int64, "to_version", cur,
			"note", "stored events keep their stamped model_family until: tatitok recompute --model-map")
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// beginWrite starts an immediate (write) transaction for the Open path,
// retrying SQLITE_BUSY with backoff up to a 5s deadline — matching the
// busy_timeout every other statement gets. The driver's busy handler
// covers contention on an ESTABLISHED database (ingest-time writers
// wait correctly), but during bootstrap — concurrent first opens
// converting a fresh file to WAL — BEGIN IMMEDIATE can return
// SQLITE_BUSY instantly without consulting the handler (the
// mixed-journal transition window; reproduced by TestOpenConcurrent
// under -count=200 -race, M4 Codex round finding 1). A BUSY here always
// means "another opener holds the write lock", which always resolves,
// so the retry makes bootstrap serialization deterministic rather than
// timing-dependent.
func beginWrite(db *sql.DB) (*sql.Tx, error) {
	deadline := time.Now().Add(5 * time.Second)
	wait := time.Millisecond
	for {
		tx, err := db.Begin()
		if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") || time.Now().After(deadline) {
			return tx, err
		}
		time.Sleep(wait)
		if wait < 50*time.Millisecond {
			wait *= 2
		}
	}
}

func migrate(db *sql.DB) error { return migrateTo(db, len(migrations)) }

// migrateTo applies pending migrations up to (and excluding) index target.
// Split out so the upgrade tests can build a database frozen at an older
// schema version and then let Open finish the job.
//
// Concurrency (M3.1 finding 3; bootstrap moved inside the lock in the
// M4 Codex round, finding 1): EVERYTHING — including creating the
// schema_version table itself on a brand-new database — happens inside
// the immediate transactions (the dsn's _txlock=immediate takes the
// write lock at BEGIN), and the version is re-read per migration. Two
// processes opening the same database serialize on that lock; whoever
// enters second sees what the first committed and applies nothing
// twice. The bootstrap CREATE used to run as a bare autocommit Exec
// before the loop: under concurrent Opens that statement could surface
// SQLITE_BUSY despite the busy timeout (a write racing the serialized
// writers without holding the immediate lock) — race made
// unconstructible, not unlikely, by moving it under the same lock.
func migrateTo(db *sql.DB, target int) error {
	for {
		tx, err := beginWrite(db)
		if err != nil {
			return fmt.Errorf("begin migration tx: %w", err)
		}
		if _, err := tx.Exec(
			`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("create schema_version: %w", err)
		}
		var version int
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read schema_version: %w", err)
		}
		if version >= target {
			return tx.Rollback() // up to date — nothing was written
		}
		i := version
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if hook := migrationHooks[i]; hook != nil {
			if err := hook(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.Exec(`DELETE FROM schema_version`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
}

// SourceInfo describes one ingested file for the sources table.
type SourceInfo struct {
	Path    string
	Harness string
	// Machine is the collecting machine's label (propagated from the
	// adapter Source); the row's stable source_id is derived from
	// (Harness, Path) by core.SourceID, never stored here.
	Machine     string
	MTime       time.Time
	Size        int64
	LineCount   int
	ParseErrors int
	// ReadError non-empty marks a skipped source (could not be read);
	// stored so `sources` always reflects what the DB is missing.
	ReadError string
	// IncompleteTail: unterminated, unparseable final line (write in
	// progress); cleared by the next ingest of the same path.
	IncompleteTail bool
	// AdapterVersion is the format-handling version of the adapter that
	// parsed this file (provenance); stamped onto every event row inserted
	// through the file's transaction and onto the sources row.
	AdapterVersion int
	// MapVersion is the model-normalization map version the file's events
	// were normalized under (provenance; events only).
	MapVersion int
}

// Provenance carries the derived/provenance stamps for inserted events.
// None of these are payload: they are stamped on insert and on genuine
// replacements, but never trigger a replacement by themselves.
type Provenance struct {
	AdapterVersion int
	SourceID       string
	MapVersion     int
}

// InsertStats reports one file transaction's effect on usage_events.
type InsertStats struct {
	Inserted int // new rows (duplicates collapse on the deterministic ID)
	Replaced int // existing rows updated because their source row changed
	// TouchedDays are the UTC days (YYYY-MM-DD) of the rows actually
	// inserted or replaced — exactly the days whose rollup_daily rows
	// the triggers touched (M4 Task 3: the SSE stream tells dashboards
	// which days to refetch). Sorted; nil when nothing changed.
	TouchedDays []string
}

// FileTx is one source file's ingest transaction: events arrive in
// size-bounded batches (bounded memory — rows go to SQLite, not Go
// slices), and the file commits atomically together with its sources row.
// Rollback discards everything, so a file that fails mid-read leaves no
// partial events behind.
type FileTx struct {
	tx      *sql.Tx
	ins     *sql.Stmt
	upd     *sql.Stmt
	stats   InsertStats
	touched map[string]struct{} // UTC days written (insert or replace)
	done    bool
	quiet   bool // inherited from Store.quietReplace at BeginFile
}

// QuietReplacements switches the per-event AS-4 replacement log line
// off for this handle. The hub watcher sets it (M4 Task 1, owner-ruled):
// live re-ingest replaces rows on every pass by design, so replacements
// there are counted into the per-pass summary lines instead — reported,
// never silent (the M3.1 parity-test summarization precedent). CLI
// ingest keeps the per-event lines.
func (s *Store) QuietReplacements() { s.quietReplace = true }

// BeginFile opens the transaction for one source file's events.
func (s *Store) BeginFile(ctx context.Context) (*FileTx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO usage_events
		(id, ts, machine, source_kind, harness, provider, model, model_family,
		 project, session_id, request_id,
		 tokens_input, tokens_output, tokens_cache_write, tokens_cache_read,
		 tokens_reasoning, accuracy, meta, raw, adapter_version, source_id,
		 map_version, cost_usd_micro, cost_basis, price_snapshot, price_rates,
		 cost_api_equiv_micro)
		VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17,?18,?19,?20,?21,?22,?23,?24,?25,?26,?27)`)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	// Replacement path: some stores mutate rows in place (OpenCode updates
	// a message row while the turn is in flight), so an event ID can come
	// back with a different payload. The DB mirrors the latest source read:
	// on conflict, update iff the payload differs (NULL-safe IS NOT).
	// adapter_version, source_id, model_family, map_version and the cost
	// columns are derived/provenance, not payload — they are stamped when
	// a replacement happens but never trigger one by themselves (a
	// model-map or price-snapshot bump must never rewrite history through
	// re-ingest; that is `recompute --model-map` / `--pricing`, explicit).
	// Replacements are counted and logged, never silent (AS-4).
	upd, err := tx.PrepareContext(ctx, `UPDATE usage_events SET
		ts=?2, machine=?3, source_kind=?4, harness=?5, provider=?6, model=?7,
		model_family=?8, project=?9, session_id=?10, request_id=?11,
		tokens_input=?12, tokens_output=?13, tokens_cache_write=?14,
		tokens_cache_read=?15, tokens_reasoning=?16, accuracy=?17, meta=?18,
		raw=?19, adapter_version=?20, source_id=?21, map_version=?22,
		cost_usd_micro=?23, cost_basis=?24, price_snapshot=?25, price_rates=?26,
		cost_api_equiv_micro=?27
		WHERE id=?1 AND (
			ts IS NOT ?2 OR machine IS NOT ?3 OR source_kind IS NOT ?4 OR
			harness IS NOT ?5 OR provider IS NOT ?6 OR model IS NOT ?7 OR
			project IS NOT ?9 OR
			session_id IS NOT ?10 OR request_id IS NOT ?11 OR
			tokens_input IS NOT ?12 OR tokens_output IS NOT ?13 OR
			tokens_cache_write IS NOT ?14 OR tokens_cache_read IS NOT ?15 OR
			tokens_reasoning IS NOT ?16 OR accuracy IS NOT ?17 OR
			meta IS NOT ?18 OR raw IS NOT ?19
		)`)
	if err != nil {
		_ = ins.Close()
		_ = tx.Rollback()
		return nil, err
	}
	return &FileTx{tx: tx, ins: ins, upd: upd, quiet: s.quietReplace,
		touched: map[string]struct{}{}}, nil
}

// eventArgs encodes an event's payload into the 19 positional parameters
// (?1 id … ?19 raw) shared by the insert statement, the replacement
// statement's NULL-safe difference predicate, and the recompute verifier —
// one encoding, so "identical payload" means the same thing everywhere.
func eventArgs(e *core.Event) ([]any, error) {
	var meta any
	if e.Meta != nil {
		b, err := json.Marshal(e.Meta)
		if err != nil {
			return nil, fmt.Errorf("marshal meta for %s: %w", e.ID, err)
		}
		meta = string(b)
	}
	var raw any
	if len(e.Raw) > 0 {
		raw = string(e.Raw)
	}
	return []any{
		e.ID, e.TS.UTC().Format(time.RFC3339Nano), e.Machine, e.SourceKind,
		nullStr(e.Harness), e.Provider, e.Model, e.ModelFamily,
		nullStr(e.Project), nullStr(e.SessionID), nullStr(e.RequestID),
		e.TokensInput, e.TokensOutput, e.TokensCacheWrite, e.TokensCacheRead,
		e.TokensReasoning, string(e.Accuracy), meta, raw,
	}, nil
}

// InsertEvents validates and inserts one batch into the open transaction.
// The deterministic ID is the primary key: a duplicate with an identical
// payload is a no-op (idempotent re-ingest); a duplicate whose payload
// differs replaces the stored row to mirror the source (see BeginFile).
// An error leaves the transaction unusable; the caller must Rollback.
func (f *FileTx) InsertEvents(ctx context.Context, events []core.Event, prov Provenance) error {
	for i := range events {
		e := &events[i]
		if err := e.Validate(); err != nil {
			return err
		}
		args, err := eventArgs(e)
		if err != nil {
			return err
		}
		var rates any
		if len(e.PriceRates) > 0 {
			rates = string(e.PriceRates)
		}
		var cost, equiv any
		if e.CostUSDMicro != nil {
			cost = *e.CostUSDMicro
		}
		if e.CostAPIEquivMicro != nil {
			equiv = *e.CostAPIEquivMicro
		}
		args = append(args, nullVersion(prov.AdapterVersion),
			nullStr(prov.SourceID), nullVersion(prov.MapVersion),
			cost, nullStr(e.CostBasis), nullStr(e.PriceSnapshot), rates, equiv)
		res, err := f.ins.ExecContext(ctx, args...)
		if err != nil {
			return fmt.Errorf("insert %s: %w", e.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			f.stats.Inserted++
			f.touched[e.TS.UTC().Format("2006-01-02")] = struct{}{}
			continue
		}
		// Conflict: the row already exists. Capture its stored UTC day
		// BEFORE the update so a replacement that moves the event across a
		// day boundary invalidates BOTH days — the rollup triggers
		// subtract from the old day's bucket and add to the new, and the
		// live refresh must refetch either visible day (M6 Codex F1). A PK
		// point lookup, only on a conflict; the touched set dedups when the
		// day is unchanged (the common same-ts streaming re-emit).
		var oldDay, oldTS string
		if err := f.tx.QueryRowContext(ctx,
			`SELECT ts FROM usage_events WHERE id = ?`, e.ID).Scan(&oldTS); err == nil {
			if t, perr := time.Parse(time.RFC3339Nano, oldTS); perr == nil {
				oldDay = t.UTC().Format("2006-01-02")
			}
		}
		res, err = f.upd.ExecContext(ctx, args...)
		if err != nil {
			return fmt.Errorf("replace %s: %w", e.ID, err)
		}
		if n, err = res.RowsAffected(); err != nil {
			return err
		}
		if n == 1 {
			f.stats.Replaced++
			f.touched[e.TS.UTC().Format("2006-01-02")] = struct{}{}
			if oldDay != "" {
				f.touched[oldDay] = struct{}{} // no-op when old == new day
			}
			if !f.quiet {
				slog.Info("replaced stored event: source row changed since last ingest",
					"id", e.ID, "harness", e.Harness, "ts", e.TS.UTC())
			}
		}
	}
	return nil
}

// Commit writes the file's sources row and commits the transaction,
// returning the insert/replace counts and the touched UTC days.
func (f *FileTx) Commit(ctx context.Context, src SourceInfo) (InsertStats, error) {
	f.done = true
	f.closeStmts()
	if err := recordSource(ctx, f.tx, src); err != nil {
		_ = f.tx.Rollback()
		return InsertStats{}, err
	}
	if err := f.tx.Commit(); err != nil {
		return InsertStats{}, err
	}
	for day := range f.touched {
		f.stats.TouchedDays = append(f.stats.TouchedDays, day)
	}
	sort.Strings(f.stats.TouchedDays)
	return f.stats, nil
}

// Rollback discards the file's events (skipped source / cancelled run).
func (f *FileTx) Rollback() error {
	if f.done {
		return nil
	}
	f.done = true
	f.closeStmts()
	return f.tx.Rollback()
}

func (f *FileTx) closeStmts() {
	_ = f.ins.Close()
	_ = f.upd.Close()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func recordSource(ctx context.Context, db execer, src SourceInfo) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO sources
		(path, harness, mtime, size, line_count, ingested_at, parse_errors,
		 read_error, incomplete_tail, adapter_version, machine, source_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
			harness=excluded.harness, mtime=excluded.mtime, size=excluded.size,
			line_count=excluded.line_count, ingested_at=excluded.ingested_at,
			parse_errors=excluded.parse_errors, read_error=excluded.read_error,
			incomplete_tail=excluded.incomplete_tail,
			adapter_version=excluded.adapter_version,
			machine=excluded.machine, source_id=excluded.source_id`,
		src.Path, src.Harness, src.MTime.UTC().Format(time.RFC3339Nano),
		src.Size, src.LineCount, time.Now().UTC().Format(time.RFC3339Nano),
		src.ParseErrors, nullStr(src.ReadError), src.IncompleteTail,
		nullVersion(src.AdapterVersion), nullStr(src.Machine),
		core.SourceID(src.Harness, src.Path)); err != nil {
		return fmt.Errorf("record source %s: %w", src.Path, err)
	}
	return nil
}

// RecordSource upserts a sources row outside any file transaction — used
// for skipped sources (read errors) and zero-event files.
func (s *Store) RecordSource(ctx context.Context, src SourceInfo) error {
	return recordSource(ctx, s.db, src)
}

// InsertBatch writes one file's events and its sources row in a single
// transaction (convenience wrapper over BeginFile/InsertEvents/Commit).
func (s *Store) InsertBatch(ctx context.Context, events []core.Event, src SourceInfo) (InsertStats, error) {
	f, err := s.BeginFile(ctx)
	if err != nil {
		return InsertStats{}, err
	}
	if err := f.InsertEvents(ctx, events, Provenance{
		AdapterVersion: src.AdapterVersion,
		SourceID:       core.SourceID(src.Harness, src.Path),
		MapVersion:     src.MapVersion,
	}); err != nil {
		_ = f.Rollback()
		return InsertStats{}, fmt.Errorf("%s: %w", src.Path, err)
	}
	return f.Commit(ctx, src)
}

func nullVersion(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
