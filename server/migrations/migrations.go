// Package migrations holds Workbench's own tables. They are applied by kagent's
// migration runner, after kagent's built-in tracks, so one run leaves the
// whole database at one version.
package migrations

import "embed"

// FS holds the goose migration files.
//
//go:embed *.sql
var FS embed.FS

// Schema is the PostgreSQL schema Workbench's tables live in, separate from
// kagent's so that neither can collide with the other.
const Schema = "workbench"

// TrackingTable records which Workbench migrations have run.
const TrackingTable = "workbench_migrations"
