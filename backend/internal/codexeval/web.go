package codexeval

import (
	_ "embed"
	"encoding/json"
	"html"
	"strings"
)

const pluginVersion = "0.4.6"

//go:embed web/ui.html
var uiTemplate string

//go:embed web/style.css
var uiStyles string

//go:embed web/theme.css
var uiThemes string

//go:embed web/vendor/markdown-it.min.js
var markdownScript string

//go:embed web/vendor/markdown-it-cjk-friendly.min.js
var cjkFriendlyScript string

//go:embed web/vendor/texmath.js
var texmathScript string

//go:embed web/vendor/temml.min.js
var temmlScript string

//go:embed web/vendor/temml.css
var temmlStyles string

//go:embed web/storage.js
var storageScript string

//go:embed web/credentials.js
var credentialsScript string

//go:embed web/app.js
var appScript string

//go:embed web/catalog.js
var catalogScript string

//go:embed web/components.js
var componentsScript string

//go:embed web/candy.js
var candyScript string

//go:embed web/siwc.js
var siwcScript string

//go:embed web/fingerprint.js
var fingerprintScript string

//go:embed web/pelican.js
var pelicanScript string

//go:embed web/inspection.js
var inspectionScript string

var uiHTML = func() []byte {
	config, _ := json.Marshal(map[string]any{"requests": fingerprintTarget, "aliases": fingerprintAliases})
	fpScript := strings.Replace(fingerprintScript, `/*FINGERPRINT_CONFIG*/{}`, string(config), 1)
	variants, _ := json.Marshal(pelicanVariants)
	plScript := strings.Replace(pelicanScript, `/*PELICAN_VARIANTS*/[]`, string(variants), 1)
	return []byte(strings.NewReplacer(
		"/*APP_STYLES*/", temmlStyles+"\n"+uiThemes+"\n"+uiStyles,
		"<!--PLUGIN_VERSION-->", pluginVersion,
		"<!--CANDY_PROMPT-->", html.EscapeString(candyPrompt),
		"<!--FINGERPRINT_LICENSE-->", "<!-- lm-detector\n"+detectorLicense+"-->",
		"/*MARKDOWN_SCRIPT*/", markdownScript,
		"/*CJK_FRIENDLY_SCRIPT*/", cjkFriendlyScript,
		"/*TEXMATH_SCRIPT*/", texmathScript,
		"/*TEMML_SCRIPT*/", temmlScript,
		"/*STORAGE_SCRIPT*/", storageScript,
		"/*CREDENTIALS_SCRIPT*/", credentialsScript,
		"/*CATALOG_SCRIPT*/", catalogScript,
		"/*COMPONENTS_SCRIPT*/", componentsScript,
		"/*CANDY_SCRIPT*/", candyScript,
		"/*SIWC_SCRIPT*/", siwcScript,
		"/*FINGERPRINT_SCRIPT*/", fpScript,
		"/*PELICAN_SCRIPT*/", plScript,
		"/*INSPECTION_SCRIPT*/", inspectionScript,
		"/*APP_SCRIPT*/", appScript,
	).Replace(uiTemplate))
}()

func Page() []byte { return uiHTML }
