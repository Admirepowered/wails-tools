package app

import (
	"os"

	"wailstools/internal/geodata"
	"wailstools/internal/paths"
)

// GeoIPDiag reports what the app can actually see on disk.
func (a *App) GeoIPDiag() map[string]any {
	out := map[string]any{}
	root, _ := paths.Root()
	out["root"] = root
	out["rootDir"] = a.rootDir
	db := a.geoDB()
	if db != nil {
		st := db.Status()
		out["ready"] = st.Ready
		out["path"] = st.Path
		out["size"] = st.Size
		out["error"] = st.Error
		out["downloading"] = st.Downloading
		if info, err := os.Stat(st.Path); err == nil {
			out["fileExists"] = true
			out["fileSize"] = info.Size()
			out["fileModTime"] = info.ModTime().Format("2006-01-02 15:04:05")
		} else {
			out["fileExists"] = false
			out["fileStatErr"] = err.Error()
		}
	}
	cands := geodata.CandidatePaths(root)
	existing := []string{}
	for _, c := range cands {
		if info, err := os.Stat(c); err == nil && info.Size() > 0 {
			existing = append(existing, c)
		}
	}
	out["candidates"] = cands
	out["existing"] = existing
	return out
}
