// Package web holds the dashboard frontend (ТЗ §21.4, D20): plain HTML, CSS
// and JavaScript with a local copy of ECharts, no build step.
package web

import "embed"

// FS is the frontend.
//
//go:embed index.html style.css i18n.js app.js views.js markdown.js start.js vendor/echarts.min.js vendor/ECHARTS-LICENSE vendor/ECHARTS-NOTICE vendor/fonts
var FS embed.FS
