package staticassets

import _ "embed"

//go:embed index.html
var IndexHTML string

//go:embed app.js
var AppJS string

//go:embed styles.css
var StylesCSS string
