// Package templates holds the built-in Markdown templates of the reports
// (ТЗ §21.3). A template is a {{define}} block; files of templates_dir
// replace the blocks of the same name.
package templates

import "embed"

// FS are the built-in templates.
//
//go:embed *.tmpl
var FS embed.FS
