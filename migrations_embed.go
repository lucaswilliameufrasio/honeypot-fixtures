package main

import "embed"

//go:embed migrations/*.sql
var embeddedMigrations embed.FS
