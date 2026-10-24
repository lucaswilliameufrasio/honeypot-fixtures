package main

import _ "embed"

//go:embed web/dashboard.html
var dashboardHTML []byte

//go:embed web/dashboard.js
var dashboardScript []byte

//go:embed web/dashboard.css
var dashboardStyle []byte
