package fileviewer

import (
	"regexp"
)

// BuildVersion is the canopyd version pinned into every built-in viewer
// descriptor. Injected at compile time via -ldflags when the release
// pipeline cuts a versioned binary; un-versioned dev builds fall back to
// the registry's semver floor via effectiveBuildVersion (the
// viewer_registry.version column carries a CHECK requiring semver).
var BuildVersion = "dev"

// buildVersionSemverRe mirrors the viewer_registry version CHECK exactly.
var buildVersionSemverRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$`)

// semverFloor is the version recorded for un-versioned dev builds and the
// spec's MinCanopydVersion for every built-in viewer.
const semverFloor = "0.4.0"

// IsValidSemver reports whether s satisfies the viewer_registry version
// CHECK (`^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$`).
func IsValidSemver(s string) bool {
	return buildVersionSemverRe.MatchString(s)
}

// effectiveBuildVersion returns BuildVersion when it is valid semver,
// else the semver floor (dev builds).
func effectiveBuildVersion() string {
	if IsValidSemver(BuildVersion) {
		return BuildVersion
	}
	return semverFloor
}

// seededDescriptors returns the built-in viewers with semver-safe versions.
func seededDescriptors() []BuiltInViewerDescriptor {
	v := effectiveBuildVersion()
	out := make([]BuiltInViewerDescriptor, len(AllBuiltInViewers))
	for i, d := range AllBuiltInViewers {
		d.Version = v
		d.CanopydVersion = v
		out[i] = d
	}
	return out
}

// BuildPDFJSBundleSHA256, BuildMonacoBundleSHA256 and
// BuildHandsontableBundleSHA256 pin the lazy-loaded viewer bundles. The
// bundles are built by the frontend pipeline (later phase); until they
// exist these stay empty and the registry rows carry bundle_sha256 = ”.
var (
	BuildPDFJSBundleSHA256        = ""
	BuildMonacoBundleSHA256       = ""
	BuildHandsontableBundleSHA256 = ""
)

// AllBuiltInViewers is the compile-time source of truth for the seven built-in
// viewers. This map is seeded into viewer_registry on every canopyd boot.
var AllBuiltInViewers = []BuiltInViewerDescriptor{
	{
		ViewerSlug:         "pdf",
		Version:            BuildVersion,
		CanopydVersion:     BuildVersion,
		DisplayName:        "PDF",
		Description:        "Render PDF documents with pdf.js",
		IconURL:            "/static/viewers/pdf/icon.svg",
		RenderType:         ViewerRenderFullscreen,
		SupportsMime:       []string{"application/pdf"},
		SupportsExtensions: []string{"pdf"},
		BundlePath:         "",      // pdf.js is bundled in main chunk
		BundleByteSize:     2097152, // ~2 MB
		BundleSHA256:       BuildPDFJSBundleSHA256,
		MinCanopydVersion:  "0.4.0",
	},
	{
		ViewerSlug:     "image",
		Version:        BuildVersion,
		CanopydVersion: BuildVersion,
		DisplayName:    "Image",
		Description:    "View images with lightbox, pan, pinch, rotate",
		IconURL:        "/static/viewers/image/icon.svg",
		RenderType:     ViewerRenderFullscreen,
		SupportsMime: []string{
			"image/png", "image/jpeg", "image/gif", "image/webp",
			"image/svg+xml", "image/bmp", "image/avif", "image/heic",
		},
		SupportsExtensions: []string{"png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "avif", "heic"},
		BundlePath:         "",
		BundleByteSize:     0,
		BundleSHA256:       "",
		MinCanopydVersion:  "0.4.0",
	},
	{
		ViewerSlug:     "code",
		Version:        BuildVersion,
		CanopydVersion: BuildVersion,
		DisplayName:    "Code Editor",
		Description:    "View code with Monaco Editor — 20+ languages, syntax highlighting",
		IconURL:        "/static/viewers/code/icon.svg",
		RenderType:     ViewerRenderFullscreen,
		SupportsMime: []string{
			"text/x-python", "text/x-javascript", "text/typescript",
			"text/x-go", "text/x-rust", "text/x-c", "text/x-c++",
			"text/x-java", "text/x-ruby", "text/x-php", "text/x-shellscript",
			"application/json", "application/xml", "text/x-yaml", "text/x-toml",
			"text/x-html", "text/x-css", "text/x-sql", "text/x-markdown",
		},
		SupportsExtensions: []string{
			"py", "js", "ts", "tsx", "jsx", "go", "rs", "c", "cpp", "h", "hpp",
			"java", "rb", "php", "sh", "bash", "zsh", "json", "xml", "yaml",
			"yml", "toml", "html", "css", "scss", "sql", "md", "mdx", "rs",
			"kt", "swift", "dart", "lua", "r", "jl", "ex", "exs", "elm",
		},
		BundlePath:        "/static/viewers/code/monaco.bundle.js",
		BundleByteSize:    5242880, // ~5 MB
		BundleSHA256:      BuildMonacoBundleSHA256,
		MinCanopydVersion: "0.4.0",
	},
	{
		ViewerSlug:     "csv",
		Version:        BuildVersion,
		CanopydVersion: BuildVersion,
		DisplayName:    "Spreadsheet",
		Description:    "View CSV and TSV files in a handsontable grid",
		IconURL:        "/static/viewers/csv/icon.svg",
		RenderType:     ViewerRenderFullscreen,
		SupportsMime: []string{
			"text/csv", "text/tab-separated-values",
			"application/vnd.ms-excel",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		},
		SupportsExtensions: []string{"csv", "tsv", "xlsx", "xls"},
		BundlePath:         "/static/viewers/csv/handsontable.bundle.js",
		BundleByteSize:     512000, // ~500 KB
		BundleSHA256:       BuildHandsontableBundleSHA256,
		MinCanopydVersion:  "0.4.0",
	},
	{
		ViewerSlug:         "markdown",
		Version:            BuildVersion,
		CanopydVersion:     BuildVersion,
		DisplayName:        "Markdown",
		Description:        "Render Markdown with GFM, tables, code blocks, task lists, math (KaTeX)",
		IconURL:            "/static/viewers/markdown/icon.svg",
		RenderType:         ViewerRenderFullscreen,
		SupportsMime:       []string{"text/markdown", "text/x-markdown"},
		SupportsExtensions: []string{"md", "markdown", "mdx"},
		BundlePath:         "",
		BundleByteSize:     0,
		BundleSHA256:       "",
		MinCanopydVersion:  "0.4.0",
	},
	{
		ViewerSlug:         "json",
		Version:            BuildVersion,
		CanopydVersion:     BuildVersion,
		DisplayName:        "JSON",
		Description:        "Collapsible JSON tree view with search and copy",
		IconURL:            "/static/viewers/json/icon.svg",
		RenderType:         ViewerRenderFullscreen,
		SupportsMime:       []string{"application/json"},
		SupportsExtensions: []string{"json", "jsonc", "json5"},
		BundlePath:         "",
		BundleByteSize:     0,
		BundleSHA256:       "",
		MinCanopydVersion:  "0.4.0",
	},
	{
		ViewerSlug:     "audio_video",
		Version:        BuildVersion,
		CanopydVersion: BuildVersion,
		DisplayName:    "Audio / Video",
		Description:    "HTML5 audio and video player with custom controls",
		IconURL:        "/static/viewers/audio_video/icon.svg",
		RenderType:     ViewerRenderFullscreen,
		SupportsMime: []string{
			"audio/mpeg", "audio/ogg", "audio/wav", "audio/webm", "audio/flac", "audio/aac",
			"video/mp4", "video/webm", "video/ogg", "video/quicktime",
		},
		SupportsExtensions: []string{
			"mp3", "ogg", "wav", "webm", "flac", "aac", "m4a",
			"mp4", "mov", "avi", "mkv", "webm", "ogv",
		},
		BundlePath:        "",
		BundleByteSize:    0,
		BundleSHA256:      "",
		MinCanopydVersion: "0.4.0",
	},
}

// DefaultViewerDispatch is the static MIME → viewer map used when no override matches.
var DefaultViewerDispatch = map[string]string{
	"application/pdf":           "pdf",
	"image/png":                 "image",
	"image/jpeg":                "image",
	"image/gif":                 "image",
	"image/webp":                "image",
	"image/svg+xml":             "image",
	"image/bmp":                 "image",
	"image/avif":                "image",
	"image/heic":                "image",
	"text/csv":                  "csv",
	"text/tab-separated-values": "csv",
	"application/vnd.ms-excel":  "csv",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "csv",
	"text/markdown":      "markdown",
	"text/x-markdown":    "markdown",
	"application/json":   "json",
	"audio/mpeg":         "audio_video",
	"audio/ogg":          "audio_video",
	"audio/wav":          "audio_video",
	"audio/webm":         "audio_video",
	"audio/flac":         "audio_video",
	"audio/aac":          "audio_video",
	"video/mp4":          "audio_video",
	"video/webm":         "audio_video",
	"video/ogg":          "audio_video",
	"video/quicktime":    "audio_video",
	"text/x-python":      "code",
	"text/x-javascript":  "code",
	"text/typescript":    "code",
	"text/x-go":          "code",
	"text/x-rust":        "code",
	"text/x-c":           "code",
	"text/x-c++":         "code",
	"text/x-java":        "code",
	"text/x-ruby":        "code",
	"text/x-php":         "code",
	"text/x-shellscript": "code",
	"text/x-yaml":        "code",
	"text/x-toml":        "code",
	"text/x-html":        "code",
	"text/x-css":         "code",
	"text/x-sql":         "code",
}

// ExtensionDispatch maps file extensions (lowercase, no dot) to viewer
// slugs. It is the "ext:<extension>" tier of the §6.4 dispatch order:
// an extension hit only fires when the MIME tier had no entry. Derived
// from the built-in descriptors in two passes so dedicated viewers claim
// their extensions first and the code viewer (which overlaps with
// markdown/json on md/mdx/json) only gets what remains — the table can
// therefore never drift from what the registry advertises, and shared
// extensions resolve to the dedicated viewer.
var ExtensionDispatch = func() map[string]string {
	m := make(map[string]string)
	for pass := 0; pass < 2; pass++ {
		for _, d := range AllBuiltInViewers {
			isCode := d.ViewerSlug == "code"
			if pass == 0 && isCode {
				continue
			}
			if pass == 1 && !isCode {
				continue
			}
			for _, ext := range d.SupportsExtensions {
				if _, claimed := m[ext]; !claimed {
					m[ext] = d.ViewerSlug
				}
			}
		}
	}
	return m
}()
